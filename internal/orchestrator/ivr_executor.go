package orchestrator

// IVR executor — walks an IVRFlowData v2 graph node by node, driving an
// inbound channel via the outbound ESL socket. Authored by the frontend
// flow builder (web/), persisted via /v1/flows (handlers_flows.go), and
// executed here when a call arrives on a DID that has a flow_id set.
//
// Status: SCAFFOLD. The walker + core node executors (greeting, menu,
// transfer, hangup) are wired against eslgo. The remaining node types
// (gather, http_callback, timing, goto_flow) carry TODO stubs that hang
// up gracefully so the rest of the system is testable end-to-end against
// a partial flow. See docs/PHASE_3_IVR_EXECUTION.md for the full plan.
//
// This file is not started by any main.go yet — wiring it as an outbound
// socket listener (port 8086, registered alongside the existing :8084
// masking socket) is the last go-live step. That deferral is intentional:
// it requires a live trunk (Tata or aggregator) to verify, and keeping it
// off-line until then avoids regressing the working dev stack.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
	"github.com/percipia/eslgo/command/call"

	"github.com/whatomate/tring/internal/agentleg"
	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/presence"
	"github.com/whatomate/tring/internal/queries"
)

// flowCacheTTL is how long a resolved flow definition stays cached. Flows
// change rarely (operator edits in the console), and every edit invalidates
// the key, so an hour is a safe upper bound on staleness if an invalidation
// is ever missed (e.g. the API and orchestrator point at different Redis).
const flowCacheTTL = time.Hour

// FlowCacheKey is the cache key for a flow's definition. Exported so the API
// (which writes flows) can invalidate the exact key the orchestrator reads —
// the two services share one Redis. See handlers_flows.go.
func FlowCacheKey(flowID uuid.UUID) string { return "ivr:flow:" + flowID.String() }

// flowCacheEntry is the cached payload: the owning tenant plus the raw flow
// definition JSON (kept as text so it unmarshals exactly as the DB column
// would). resolveFlowByID needs the tenant, so it's cached alongside.
type flowCacheEntry struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	Name       string    `json:"name"`
	Definition string    `json:"definition"`
}

// ─── Flow data model (matches the frontend / API JSON shape) ────────

type ivrNodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type ivrNode struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"` // see ivrNodeType constants below
	Label    string                 `json:"label"`
	Position ivrNodePosition        `json:"position"`
	Config   map[string]interface{} `json:"config"`
}

type ivrEdge struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition"` // source handle id
}

type ivrFlowData struct {
	Version   int       `json:"version"`
	Nodes     []ivrNode `json:"nodes"`
	Edges     []ivrEdge `json:"edges"`
	EntryNode string    `json:"entry_node"`
}

const (
	nodeStart        = "start"
	nodeGreeting     = "greeting"
	nodeMenu         = "menu"
	nodeGather       = "gather"
	nodeHTTPCallback = "http_callback"
	nodeTransfer     = "transfer"
	nodeGotoFlow     = "goto_flow"
	nodeTiming       = "timing"
	nodeHangup       = "hangup"
)

// ─── Executor ──────────────────────────────────────────────────────

// Executor holds the dependencies needed to drive a call through a flow:
// a DB to look up the flow by DID and (Phase 3.5) record per-step audit,
// and a logger. One Executor instance is shared across calls — it holds
// no per-call state.
type Executor struct {
	DB     *db.DB
	Q      *queries.Queries
	Logger *slog.Logger
	// Cache holds resolved flow definitions (cache-aside, 1h TTL). May be
	// nil — caching disabled, every resolve reads the DB. Set via WithCache.
	Cache *cache.Cache
	// AgentTransport / AgentGateway mirror the API: how an agent's bridge leg
	// is routed ("sip" → user/<sip>; "gateway" → the media gateway as a SIP
	// endpoint, ADR-001 M4b). Set via WithAgentTransport. Empty = "sip".
	AgentTransport string
	AgentGateway   string

	// rrMu/rr back the round_robin team strategy: a per-team counter that
	// rotates which member rings first each call. In-process (one orchestrator
	// per FreeSWITCH), reset on restart — load-spreading, not exact fairness,
	// matching the trunk round_robin precedent.
	rrMu sync.Mutex
	rr   map[string]int

	// queue holds callers waiting for a team whose members are all busy
	// (Transfer-node queue overflow). Redis-backed (shared across orchestrators)
	// when a cache is configured, else in-memory. See queue.go.
	queue Queue

	// reserve marks agents currently being rung so two concurrent dequeues —
	// within or, when Redis-backed, across orchestrators — don't both pick the
	// same idle agent (presence flips to on_call only on answer).
	reserve Reservations

	// HTTP is the client the http_callback node uses (per-request timeout comes
	// from the node config). nil → http.DefaultClient; tests inject a fake.
	HTTP *http.Client

	// eslAddr/eslPassword let the queue open a SIDE inbound ESL connection to
	// uuid_break a caller's hold music: the executor's own outbound socket is
	// blocked on the continuous playback, so it can't break it itself. Set via
	// WithESL.
	eslAddr     string
	eslPassword string
}

func (e *Executor) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

func NewExecutor(d *db.DB) *Executor {
	// Default to the in-memory backends; WithCache swaps in the Redis-backed
	// (shared, cross-orchestrator) ones when a cache is configured.
	return &Executor{DB: d, Q: queries.MustLoad(), Logger: slog.Default(), rr: map[string]int{}, queue: newMemQueue(), reserve: newMemReservations()}
}

// WithESL gives the executor the ESL credentials it needs to open a short-lived
// side connection for breaking queue hold music (see breakChannel). Returns the
// executor for chaining.
func (e *Executor) WithESL(addr, password string) *Executor {
	e.eslAddr = addr
	e.eslPassword = password
	return e
}

// WithLogger sets the executor's base logger (scoped component=orchestrator)
// and returns the executor for chaining. Per-call lines are logged through a
// child logger carrying call_id/tenant (see callState.log).
func (e *Executor) WithLogger(l *slog.Logger) *Executor {
	if l != nil {
		e.Logger = l.With("component", "orchestrator")
	}
	return e
}

// WithCache attaches a Redis cache and returns the executor for chaining. When a
// cache is configured it also swaps the queue + reservations to their Redis
// backends, so they're shared across orchestrators serving the same agents and
// survive an orchestrator restart. A nil cache leaves the in-memory backends.
func (e *Executor) WithCache(c *cache.Cache) *Executor {
	e.Cache = c
	if c != nil {
		e.queue = newRedisQueue(c)
		e.reserve = newRedisReservations(c)
	}
	return e
}

// WithAgentTransport sets how agent bridge legs are routed and returns the
// executor for chaining. Empty transport leaves the default ("sip").
func (e *Executor) WithAgentTransport(transport, gateway string) *Executor {
	e.AgentTransport = transport
	e.AgentGateway = gateway
	return e
}

// HandleInboundCall is the outbound-socket callback FreeSWITCH calls
// for each inbound channel routed to the IVR socket (default :8086 — see
// the dialplan extension in docs/PHASE_3_IVR_EXECUTION.md).
//
// Channel headers tell us the destination number; we look up the
// owning tenant + flow and walk the graph. Errors hang up with a
// hopefully-helpful cause; the caller hears NORMAL_CLEARING in success
// and CALL_REJECTED on flow misconfig.
func (e *Executor) HandleInboundCall(ctx context.Context, conn *eslgo.Conn, ev *eslgo.RawResponse) {
	uuidStr := ev.GetHeader("Unique-ID")
	called := ev.GetHeader("Caller-Destination-Number")
	caller := ev.GetHeader("Caller-Caller-ID-Number")
	// Call-scoped logger: every line for this call carries call_id so a single
	// call's IVR journey is greppable. Enriched with tenant once resolved.
	clog := e.Logger.With("call_id", uuidStr)
	clog.Info("ivr handler entry", "called", called, "caller", caller)

	if uuidStr == "" || called == "" {
		clog.Warn("ivr inbound missing uuid or destination")
		_ = conn.HangupCall(ctx, uuidStr, "CALL_REJECTED")
		return
	}

	// Two ways a flow gets resolved:
	//   - web call (visitor or agent): the flow is pinned by the tring_flow_id
	//     channel variable (set by the gwflow dialplan extension from the
	//     gateway's X-Tring-Flow-Id header), so there's no dialed DID to look up.
	//   - PSTN/DID call: discover the flow from the dialed inbound number.
	var resolved *ResolvedFlow
	var err error
	webCall := false
	if fid := ev.GetHeader("variable_tring_flow_id"); fid != "" {
		webCall = true
		clog.Info("ivr web call, flow pinned", "flow", fid)
		resolved, err = e.resolveFlowByID(ctx, fid)
	} else {
		resolved, err = e.resolveFlow(ctx, called)
	}
	if err != nil {
		clog.Warn("ivr no flow resolved", "called", called, "err", err)
		_ = conn.HangupCall(ctx, uuidStr, "UNALLOCATED_NUMBER")
		return
	}
	if resolved.Flow == nil {
		clog.Warn("ivr no flow attached, hanging up", "called", called)
		_ = conn.HangupCall(ctx, uuidStr, "NORMAL_CLEARING")
		return
	}
	tenantID := resolved.TenantID
	flow := *resolved.Flow
	clog = clog.With("tenant", tenantID)

	// A web call has no dialed number — it routed to the internal `gwflow`
	// extension. Show the flow it reached as the call log's "To" instead of
	// the meaningless routing token.
	if webCall && resolved.FlowName != "" {
		called = resolved.FlowName
	}

	state := &callState{
		uuid:      uuidStr,
		tenantID:  tenantID,
		caller:    caller,
		called:    called,
		clientRef: ev.GetHeader("variable_tring_client_ref"),
		vars:      map[string]string{},
		log:       clog,
	}

	// Log the inbound/IVR call (incl. guest web calls) so it shows in the
	// call log — previously only the outbound path wrote a calls row. The
	// global event loop (runner.go) then settles it (answered/ended/
	// duration/webhooks) by fs_uuid, same as outbound. Insert while still
	// ringing, before answering, so the CHANNEL_ANSWER from AnswerCall finds
	// the row to flip. Best-effort: never block the call on a logging error.
	e.recordInboundCall(ctx, state)
	// Persist the IVR path on the way out, even if walk errors mid-flow.
	defer e.saveIVRPath(state)

	clog.Info("ivr resolved flow, answering", "to", called)
	if err := conn.AnswerCall(ctx, uuidStr); err != nil {
		clog.Error("ivr answer failed", "err", err)
		return
	}
	clog.Info("ivr answered, walking flow", "entry_node", flow.EntryNode)

	if err := e.walk(ctx, conn, &flow, flow.EntryNode, state); err != nil {
		clog.Error("ivr walk error", "err", err)
	}

	// Best-effort hangup if the call's still up. Hangup nodes already do
	// this; this catches paths that fall through (e.g. no outgoing edge).
	_ = conn.HangupCall(ctx, uuidStr, "NORMAL_CLEARING")
}

// ResolvedFlow is what resolveFlow returns: the tenant that owns the
// dialed DID plus the parsed flow definition, or Flow=nil when the
// number exists but has no flow attached. Exported so the orchestrator
// tests can exercise the lookup without a fake ESL connection.
type ResolvedFlow struct {
	TenantID uuid.UUID
	Flow     *ivrFlowData
	FlowName string // flows.name — shown as the call log "To" for web calls
}

// resolveFlow looks up the inbound DID in `numbers`, joins to `flows`,
// and parses the JSON definition. Returns:
//
//   - error              the number isn't registered (or the row scan failed)
//   - (TenantID, nil)    number registered but has no active flow
//   - (TenantID, &flow)  number registered with an active, valid flow
//
// Errors from JSON decoding or empty entry_node are returned so the
// caller can pick a reject cause; lookup failures bubble pgx errors so
// "not found" is distinguishable from "DB down".
func (e *Executor) resolveFlow(ctx context.Context, called string) (*ResolvedFlow, error) {
	var (
		tenantID uuid.UUID
		flowID   *uuid.UUID
	)
	// Cheap indexed point read; always hits the DB (a number's flow
	// assignment can change independently of the flow's contents).
	row := e.DB.Pool.QueryRow(ctx, e.Q.ResolveInboundNumber, called)
	if err := row.Scan(&tenantID, &flowID); err != nil {
		return nil, err
	}
	if flowID == nil {
		return &ResolvedFlow{TenantID: tenantID}, nil
	}
	// The (large, rarely-changing) definition is served from the cache.
	// A nil/empty flow means the attached flow is inactive — same outcome
	// as the old LEFT JOIN ... AND is_active returning no definition.
	flow, _, name, err := e.loadFlow(ctx, *flowID)
	if err != nil {
		return nil, err
	}
	return &ResolvedFlow{TenantID: tenantID, Flow: flow, FlowName: name}, nil
}

// recordInboundCall inserts a calls row for an inbound/IVR call so it appears
// in the call log. The orchestrator's global event loop settles it
// (answered/ended/duration/webhooks) by fs_uuid afterwards. Best-effort: a
// failure is logged and the call proceeds — logging must never break a call.
func (e *Executor) recordInboundCall(ctx context.Context, st *callState) {
	var id uuid.UUID
	err := e.DB.Pool.QueryRow(ctx, e.Q.CreateCallInbound,
		st.tenantID, st.caller, st.called, st.uuid, st.clientRef,
	).Scan(&id)
	if err != nil {
		st.log.Error("ivr failed to record inbound call", "err", err)
		return
	}
	st.log.Info("ivr recorded inbound call", "call_row", id)
}

// saveIVRPath persists the recorded IVR journey to the call's ivr_path column,
// matched by FreeSWITCH channel UUID. Called from a defer, so it uses a fresh
// short context (the call's context is cancelled once the call ends).
// Best-effort: the column defaults to '[]', so a missed write just shows an
// empty path in the log.
func (e *Executor) saveIVRPath(st *callState) {
	if len(st.steps) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := json.Marshal(st.steps)
	if err != nil {
		st.log.Error("ivr marshal ivr_path failed", "err", err)
		return
	}
	if _, err := e.DB.Pool.Exec(ctx, e.Q.SetCallIVRPath, string(b), st.uuid); err != nil {
		st.log.Error("ivr save ivr_path failed", "err", err)
	}
}

// resolveFlowByID loads a flow directly by its UUID — the guest web-call
// path, where the flow is pinned via the tring_flow_id channel variable
// rather than discovered from a dialed DID. Mirrors resolveFlow's return
// contract (error on lookup/parse failure; Flow=nil if the flow row exists
// but is empty).
func (e *Executor) resolveFlowByID(ctx context.Context, flowID string) (*ResolvedFlow, error) {
	fid, err := uuid.Parse(flowID)
	if err != nil {
		return nil, fmt.Errorf("bad flow id %q: %w", flowID, err)
	}
	flow, tenantID, name, err := e.loadFlow(ctx, fid)
	if err != nil {
		return nil, err
	}
	if tenantID == uuid.Nil {
		// No active flow with that id — preserve the old not-found contract
		// (resolveFlowByID's caller distinguishes this from a parse error).
		return nil, pgx.ErrNoRows
	}
	return &ResolvedFlow{TenantID: tenantID, Flow: flow, FlowName: name}, nil
}

// loadFlow returns the parsed definition for a flow id, cache-aside (Redis,
// 1h TTL). On a hit it parses the cached JSON; on a miss it reads the active
// flow from Postgres and populates the cache. Returns:
//
//   - (nil, uuid.Nil, nil)   no active flow with that id
//   - (nil, tenantID, nil)   active flow row, but empty definition
//   - (flow, tenantID, nil)  active flow, parsed
//   - (nil, _, err)          DB error, or the definition fails to parse
//
// The raw bytes are cached before parsing, so a malformed-but-stored flow
// returns the same deterministic parse error on every call until it's
// edited (which invalidates the key) — it never silently disappears.
func (e *Executor) loadFlow(ctx context.Context, flowID uuid.UUID) (*ivrFlowData, uuid.UUID, string, error) {
	key := FlowCacheKey(flowID)

	if b, ok := e.Cache.Get(ctx, key); ok {
		var ent flowCacheEntry
		if json.Unmarshal(b, &ent) == nil {
			flow, err := parseFlowDef([]byte(ent.Definition))
			return flow, ent.TenantID, ent.Name, err
		}
		// Corrupt cache entry — fall through to the DB and overwrite it.
	}

	var (
		tenantID uuid.UUID
		name     string
		defJSON  []byte
	)
	row := e.DB.Pool.QueryRow(ctx, e.Q.GetActiveFlowByID, flowID)
	if err := row.Scan(&tenantID, &name, &defJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, uuid.Nil, "", nil // no active flow
		}
		return nil, uuid.Nil, "", err
	}

	if ent, err := json.Marshal(flowCacheEntry{TenantID: tenantID, Name: name, Definition: string(defJSON)}); err == nil {
		e.Cache.Set(ctx, key, ent, flowCacheTTL)
	}

	if len(defJSON) == 0 {
		return nil, tenantID, name, nil
	}
	flow, err := parseFlowDef(defJSON)
	return flow, tenantID, name, err
}

// parseFlowDef unmarshals a flow definition and enforces the one invariant
// the executor relies on: a non-empty entry node.
func parseFlowDef(defJSON []byte) (*ivrFlowData, error) {
	if len(defJSON) == 0 {
		return nil, nil
	}
	var flow ivrFlowData
	if err := json.Unmarshal(defJSON, &flow); err != nil {
		return nil, fmt.Errorf("bad flow JSON: %w", err)
	}
	if flow.EntryNode == "" {
		return nil, fmt.Errorf("flow has no entry node")
	}
	return &flow, nil
}

// callState is the per-call mutable state — variables gathered via
// Gather nodes, the caller/callee identifiers, and tenant context.
type callState struct {
	uuid      string
	tenantID  uuid.UUID
	caller    string
	called    string
	clientRef string // embedder-supplied caller identity (tring_client_ref)
	vars      map[string]string
	// queuePriority routes this caller into a team's priority queue tier rather
	// than the normal tier. Set from the Transfer node's Priority checkbox; the
	// graph (which Transfer the call reaches) decides it — e.g. a caller-lookup
	// success branch points at a priority Transfer.
	queuePriority bool

	// log is the per-call logger: the executor's base logger with call_id +
	// tenant bound, so every walk/node line correlates to this one call.
	log *slog.Logger

	// steps is the recorded IVR journey (one SimStep per node walked),
	// persisted to calls.ivr_path so the call log shows the path taken.
	// Reuses the simulator's step shape so the live path and the Test-panel
	// trace render identically.
	steps []SimStep
	// Per-node scratch the node handlers fill in (action/detail/pressed
	// digit); walk() reads them to build the step, then clears them before
	// the next node. Keeps runNode's (outcome, error) signature unchanged.
	stepAction string
	stepDetail string
	stepDTMF   string
}

// templateVars returns the variables an http_callback may interpolate: the
// collected flow vars plus the call's built-in identity fields (caller/called/
// client_ref). Flow vars win on a name clash.
func (st *callState) templateVars() map[string]string {
	m := make(map[string]string, len(st.vars)+3)
	m["caller"] = st.caller
	m["called"] = st.called
	m["client_ref"] = st.clientRef
	for k, v := range st.vars {
		m[k] = v
	}
	return m
}

// recordStep appends one step to the IVR journey from the scratch fields the
// node handler set plus the resolved outcome.
func (st *callState) recordStep(node *ivrNode, outcome string) {
	st.steps = append(st.steps, SimStep{
		NodeID:    node.ID,
		NodeType:  node.Type,
		Action:    st.stepAction,
		Detail:    st.stepDetail,
		DTMFInput: st.stepDTMF,
		Outcome:   outcome,
	})
}

// walk runs the node identified by nodeID, then follows the outgoing
// edge corresponding to the node's outcome and recurses. Cycles are not
// detected — flows are caller-authored and a cycle would be a flow bug;
// the FreeSWITCH side will hang up after the per-call media timeout.
//
// Max depth: 64 (prevents runaway recursion).
func (e *Executor) walk(ctx context.Context, conn *eslgo.Conn, flow *ivrFlowData, nodeID string, st *callState) error {
	for depth := 0; depth < 64; depth++ {
		node, ok := findNode(flow, nodeID)
		if !ok {
			return fmt.Errorf("node %s not found", nodeID)
		}
		st.log.Info("ivr walk node", "depth", depth, "node", node.ID, "type", node.Type)

		// Clear per-node scratch; the handler fills it in, then we record
		// one step from it below (after the outcome is known).
		st.stepAction, st.stepDetail, st.stepDTMF = "", "", ""
		outcome, err := e.runNode(ctx, conn, node, st)
		if err != nil {
			st.recordStep(node, "error")
			return fmt.Errorf("node %s (%s): %w", node.ID, node.Type, err)
		}
		st.log.Info("ivr node outcome", "node", node.ID, "outcome", outcome)
		st.recordStep(node, outcome)
		if outcome == "" {
			// Terminal node (hangup) or executor explicitly ended.
			return nil
		}

		next := findNextNode(flow, nodeID, outcome)
		if next == "" {
			st.log.Info("ivr node has no outgoing edge, ending", "node", node.ID, "outcome", outcome)
			return nil
		}
		nodeID = next
	}
	return errors.New("flow walk exceeded max depth (64)")
}

func findNode(flow *ivrFlowData, id string) (*ivrNode, bool) {
	for i := range flow.Nodes {
		if flow.Nodes[i].ID == id {
			return &flow.Nodes[i], true
		}
	}
	return nil, false
}

func findNextNode(flow *ivrFlowData, fromID, condition string) string {
	for _, e := range flow.Edges {
		if e.From == fromID && e.Condition == condition {
			return e.To
		}
	}
	return ""
}

// runNode dispatches to the node-type-specific executor. Returns the
// outcome string (matches edge.condition / source handle id) or "" for
// terminal nodes (hangup). An empty string with no error means "end the
// call gracefully" — used by Hangup.
func (e *Executor) runNode(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	switch node.Type {
	case nodeStart:
		// No-op passthrough — the Start node is purely a UI anchor so
		// flows have a fixed entry point. Walk to whatever's wired to
		// its "default" output.
		st.stepAction = "passthrough"
		return "default", nil
	case nodeGreeting:
		return e.runGreeting(ctx, conn, node, st)
	case nodeMenu:
		return e.runMenu(ctx, conn, node, st)
	case nodeTransfer:
		return e.runTransfer(ctx, conn, node, st)
	case nodeHangup:
		return e.runHangup(ctx, conn, node, st)

	// ─── TODO scaffolds (see docs/PHASE_3_IVR_EXECUTION.md) ──────────
	case nodeGather:
		// TODO: play prompt → read N digits up to terminator → st.vars[store_as] = digits → "default" / "timeout" / "max_retries"
		st.log.Warn("ivr node not yet implemented, following default outcome", "type", node.Type, "node", node.ID, "outcome", "default")
		return "default", nil
	case nodeHTTPCallback:
		return e.runHTTPCallback(ctx, conn, node, st)
	case nodeTiming:
		// TODO: evaluate config.schedule against time.Now in tenant TZ → "in_hours" / "out_of_hours"
		st.log.Warn("ivr node not yet implemented, following default outcome", "type", node.Type, "node", node.ID, "outcome", "in_hours")
		return "in_hours", nil
	case nodeGotoFlow:
		// TODO: load flow_id, recurse into walk() against new flow.entry_node
		st.log.Warn("ivr node not yet implemented, ending call", "type", node.Type, "node", node.ID)
		return "", nil

	default:
		return "", fmt.Errorf("unknown node type %q", node.Type)
	}
}

// ─── Node implementations (real) ────────────────────────────────────

func (e *Executor) runGreeting(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	st.log.Info("ivr greeting", "node", node.ID)
	if file, ok := node.Config["audio_file"].(string); ok && file != "" {
		st.stepAction, st.stepDetail = "play_audio", file
		st.log.Info("ivr playback (sync)", "node", node.ID, "file", file)
		_, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "playback", AppArgs: file, Sync: true})
		if err != nil {
			return "", fmt.Errorf("playback: %w", err)
		}
		st.log.Info("ivr playback done", "node", node.ID, "file", file)
	} else if text, ok := node.Config["greeting_text"].(string); ok && text != "" {
		st.stepAction, st.stepDetail = "speak", text
		_, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "speak", AppArgs: "flite|kal|" + text, Sync: true})
		if err != nil {
			return "", fmt.Errorf("speak: %w", err)
		}
	}
	return "default", nil
}

func (e *Executor) runMenu(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	timeoutSec := intFromConfig(node.Config, "timeout_seconds", 10)
	maxRetries := intFromConfig(node.Config, "max_retries", 3)
	options := mapFromConfig(node.Config, "options")
	if len(options) == 0 {
		return "", errors.New("menu has no options")
	}

	// Build the valid-digit regexp from configured options.
	validDigits := make([]string, 0, len(options))
	for digit := range options {
		validDigits = append(validDigits, digit)
	}
	regex := "^[" + strings.Join(validDigits, "") + "]$"

	prompt := stringFromConfig(node.Config, "audio_file")
	if prompt == "" {
		prompt = "silence_stream://500"
	}

	// play_and_get_digits min max retries timeout terminators sound_file invalid_file var regexp
	args := fmt.Sprintf("1 1 %d %d # %s silence_stream://500 ivr_choice %s",
		maxRetries, timeoutSec*1000, prompt, regex)
	if _, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "play_and_get_digits", AppArgs: args, Sync: true}); err != nil {
		return "", fmt.Errorf("play_and_get_digits: %w", err)
	}

	st.stepAction = "prompt_dtmf"
	digit, err := getChannelVar(ctx, conn, st.uuid, "ivr_choice")
	st.stepDTMF = digit // recorded even when empty (timeout) or invalid
	if err != nil || digit == "" {
		return "timeout", nil
	}
	if _, ok := options[digit]; !ok {
		return "max_retries", nil
	}
	return "digit:" + digit, nil
}

func (e *Executor) runTransfer(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	st.stepAction = "transfer"

	// What the agent's incoming-call screen shows: the trusted client_ref if we
	// have one, else a generic label — never the gateway's internal SIP identity
	// (which is why agents saw "sipgo"). Carried as an X-Tring-Agent-Label SIP
	// header on the agent leg; the gateway reads it in preference to the From.
	label := st.clientRef
	if label == "" {
		label = "Web Caller"
	}

	// Team target → strategy-ordered ring with queue overflow. The Priority
	// checkbox (config.priority) puts the caller in the team's priority queue
	// tier. "Who is calling decides priority" is expressed in the graph, not a
	// variable: wire a caller lookup's success branch to a Transfer with Priority
	// on, and its failure branch to a normal Transfer.
	if team := stringFromConfig(node.Config, "team"); team != "" {
		st.queuePriority = boolFromConfig(node.Config, "priority")
		st.stepDetail = "team " + team
		if st.queuePriority {
			st.stepDetail += " (priority)"
		}
		return e.runTeamTransfer(ctx, conn, st, team, label)
	}

	// Legacy: a single pinned agent (agent_sip) or ring-all (no team). No queue.
	if sip := stringFromConfig(node.Config, "agent_sip"); sip != "" {
		st.stepDetail = sip
	} else {
		st.stepDetail = "all agents"
	}
	bridgeTo, sips, err := e.agentBridgeTarget(ctx, node, st.tenantID, label)
	if err != nil {
		st.log.Error("ivr transfer target lookup failed", "node", node.ID, "err", err)
		return "no_answer", nil
	}
	if bridgeTo == "" {
		st.log.Warn("ivr transfer has no agents to ring", "node", node.ID)
		return "no_answer", nil
	}
	st.log.Info("ivr transfer bridging", "node", node.ID, "bridge_to", bridgeTo, "agents", sips)

	if _, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "bridge", AppArgs: bridgeTo, Sync: true}); err != nil {
		return "", fmt.Errorf("bridge: %w", err)
	}
	// Source of truth for "did an agent answer" is the CHANNEL_BRIDGE event
	// (handled in runner.go), which stamps calls.agent_id with the answering
	// agent during the bridge. We read it back here rather than probing a
	// post-bridge channel variable — the latter is unreliable for web callers
	// (e.g. if the guest hangs up first, the A-leg is gone and the read fails,
	// which is exactly the "answered but logged no_answer" bug). A fresh
	// context is used because the call's ctx may already be cancelled.
	_ = sips
	if label := e.answeredAgentLabel(st.uuid); label != "" {
		st.stepDetail = label // show the agent who answered in the IVR path
		return "completed", nil
	}
	return "no_answer", nil
}

// queueDepthTTL bounds how long a published queue-depth value lives without a
// refresh. The orchestrator re-publishes on every poll tick while callers wait,
// so a team's count goes stale (then expires to "absent" = 0) within this window
// of the last caller leaving — and survives an orchestrator crash without leaving
// a phantom count forever.
const queueDepthTTL = 10 * time.Second

// QueueDepthKey is the Redis key holding the number of callers waiting in a
// team's queue. The orchestrator writes it (publishDepth); the API reads it for
// the dashboard. Exported so both sides agree on the key (like FlowCacheKey).
func QueueDepthKey(tenantID uuid.UUID, team string) string {
	return "tring:queue:depth:" + tenantID.String() + ":" + team
}

// queueKey / resKey build the per-tenant in-process keys for the call queue (by
// team) and agent reservations (by sip).
func queueKey(tenantID uuid.UUID, team string) string { return tenantID.String() + "/" + team }
func resKey(tenantID uuid.UUID, sip string) string    { return tenantID.String() + "/" + sip }

// publishDepth writes a team's current queue depth to Redis so the API/dashboard
// can show it. No-op without a cache.
func (e *Executor) publishDepth(ctx context.Context, tenantID uuid.UUID, team string) {
	if e.Cache == nil {
		return
	}
	n := e.queue.depth(ctx, queueKey(tenantID, team))
	e.Cache.Set(ctx, QueueDepthKey(tenantID, team), []byte(strconv.Itoa(n)), queueDepthTTL)
}

// queuePollInterval is how often a queued caller's watcher re-checks for a free
// agent. queueSettleDelay lets the caller channel exit the broken hold-music
// playback before bridging, so FreeSWITCH doesn't abandon the agent-leg INVITE.
const (
	queuePollInterval = 2 * time.Second
	queueSettleDelay  = 250 * time.Millisecond
)

// runTeamTransfer rings a team by its strategy and, when every member is busy,
// holds the caller in the team's queue (music-on-hold) until a member frees up
// or max_wait elapses. Outcomes: "completed" (an agent answered), "queue_timeout"
// (waited too long), "no_answer" (unknown/empty team), or "" (caller abandoned
// — terminal; runner.go settles the CDR). The caller is already answered.
func (e *Executor) runTeamTransfer(ctx context.Context, conn *eslgo.Conn, st *callState, team, label string) (string, error) {
	cfg, ok, err := e.loadTeamConfig(ctx, st.tenantID, team)
	if err != nil {
		st.log.Error("ivr transfer team lookup failed", "team", team, "err", err)
		return "no_answer", nil
	}
	if !ok {
		st.log.Warn("ivr transfer unknown team, routing no_answer", "team", team)
		return "no_answer", nil
	}
	members, err := e.teamMemberSips(ctx, st.tenantID, team)
	if err != nil {
		st.log.Error("ivr transfer team members lookup failed", "team", team, "err", err)
		return "no_answer", nil
	}
	if len(members) == 0 {
		st.log.Warn("ivr transfer team has no available members, routing no_answer", "team", team)
		return "no_answer", nil
	}
	var domain string
	if err := e.DB.Pool.QueryRow(ctx, e.Q.GetTenantSipDomain, st.tenantID).Scan(&domain); err != nil {
		return "no_answer", nil
	}
	if cfg.maxWait <= 0 {
		cfg.maxWait = 120
	}

	qkey := queueKey(st.tenantID, team)
	// Stage 3 sets priority from the caller's backend lookup; Stage 2 is all-normal.
	priority := st.queuePriority
	waiter := e.queue.enqueue(ctx, qkey, priority)
	defer func() {
		// Background ctx: this runs on exit, when the call ctx may already be done.
		bg := context.Background()
		e.queue.remove(bg, qkey, waiter)
		e.publishDepth(bg, st.tenantID, team) // reflect this caller leaving
	}()

	// Surface the resolved hold-music source so a silent/unreadable MOH is
	// diagnosable from the log alone.
	st.log.Info("ivr transfer queued, holding caller", "team", team, "moh", cfg.moh, "priority", priority, "max_wait_s", cfg.maxWait)

	deadline := time.Now().Add(time.Duration(cfg.maxWait) * time.Second)
	for {
		// Liveness probe: the queue wait can outlast the caller. uuid_exists (plus
		// ctx) makes an abandoned call exit immediately instead of holding a slot.
		if !channelAlive(ctx, conn, st.uuid) {
			st.log.Info("ivr transfer: caller gone while queued", "team", team)
			st.stepDetail = "abandoned in queue"
			return "", nil
		}

		// Dequeue: a member is free AND we're at the head of line. Nothing is
		// playing here (the hold music below has returned), so we bridge directly.
		avail := e.filterAvailable(ctx, st.tenantID, members)
		if len(avail) > 0 && e.queue.isHead(ctx, qkey, waiter) {
			e.queue.remove(ctx, qkey, waiter)      // release our spot so the next waiter can claim too
			e.publishDepth(ctx, st.tenantID, team) // dashboard: one fewer waiting
			// The continuous hold music was just broken by the side connection — let
			// the channel settle before bridging so FreeSWITCH doesn't abandon the
			// agent-leg INVITE ("transaction terminated").
			select {
			case <-ctx.Done():
				return "", nil
			case <-time.After(queueSettleDelay):
			}
			// Keep audio going through the ring: hand the hold music to FreeSWITCH as
			// the bridge ringback so the caller hears it WHILE the agent rings, and FS
			// stops it the instant the agent answers (no dead air, no bleed-through).
			_, _ = conn.SendCommand(ctx, &call.Set{UUID: st.uuid, Key: "ringback", Value: cfg.moh, Sync: false})
			_, _ = conn.SendCommand(ctx, &call.Set{UUID: st.uuid, Key: "instant_ringback", Value: "true", Sync: false})
			dial := e.buildTeamDial(ctx, st.tenantID, team, cfg.strategy, cfg.ringSeconds, avail, domain, label)
			// Reserve the agents we're about to ring so a concurrently-dequeuing
			// caller queues instead of double-ringing the same idle agent (presence
			// flips to on_call only on answer). Released after the ring so a
			// fall-through frees them immediately, while an answered agent stays
			// excluded via presence on_call.
			release := e.reserveAgents(ctx, st.tenantID, avail, time.Duration(cfg.ringSeconds+5)*time.Second)
			st.log.Info("ivr transfer bridging team", "team", team, "strategy", cfg.strategy, "targets", len(avail))
			_, bridgeErr := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "bridge", AppArgs: dial, Sync: true})
			release()
			if bridgeErr != nil {
				return "", fmt.Errorf("bridge: %w", bridgeErr)
			}
			if l := e.answeredAgentLabel(st.uuid); l != "" {
				st.stepDetail = l
				return "completed", nil
			}
			// Nobody answered (raced busy / declined) — rejoin the line and wait.
			st.log.Info("ivr transfer team bridge fell through, requeueing", "team", team)
			waiter = e.queue.enqueue(ctx, qkey, priority)
			continue
		}

		if time.Now().After(deadline) {
			st.log.Info("ivr transfer queue timed out", "team", team, "max_wait_s", cfg.maxWait)
			st.stepDetail = "team " + team + " (queue timeout)"
			return "queue_timeout", nil
		}

		e.publishDepth(ctx, st.tenantID, team) // keep the dashboard count fresh while waiting

		// Hold the caller with one continuous, gapless playback broken by the side
		// ESL connection the instant we're ready; the loop then re-evaluates and
		// bridges (after the settle delay above).
		e.holdWithMusic(ctx, conn, st, team, cfg.moh, members, qkey, waiter, deadline)
	}
}

// holdWithMusic plays ONE continuous, gapless hold-music playback and returns
// when a watcher breaks it from the side ESL connection (a member became free
// and we're head, or the deadline passed) or it ends on its own (caller hung up
// / one-shot file finished). The caller's loop then re-evaluates and, after a
// settle delay, bridges.
func (e *Executor) holdWithMusic(ctx context.Context, conn *eslgo.Conn, st *callState, team, moh string, members []string, qkey, waiter string, deadline time.Time) {
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(queuePollInterval)
		defer t.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-t.C:
				e.publishDepth(watchCtx, st.tenantID, team)
				ready := len(e.filterAvailable(watchCtx, st.tenantID, members)) > 0 && e.queue.isHead(watchCtx, qkey, waiter)
				if ready || time.Now().After(deadline) {
					st.log.Info("ivr queue: breaking hold music", "team", team, "ready", ready)
					e.breakChannel(st.uuid)
					return
				}
			}
		}
	}()
	// Bound the playback just past the deadline so a caller can't stick on hold
	// forever if the break ever fails to fire.
	playCtx, cancel := context.WithDeadline(ctx, deadline.Add(5*time.Second))
	defer cancel()
	if _, err := conn.SendCommand(playCtx, &call.Execute{UUID: st.uuid, AppName: "playback", AppArgs: moh, Sync: true}); err != nil {
		st.log.Warn("ivr queue: hold-music playback error", "moh", moh, "err", err)
	}
	st.log.Info("ivr queue: hold music returned", "team", team)
}

// breakChannel stops a channel's current playback from a FRESH inbound ESL
// connection — the executor's own outbound socket is blocked on the gapless
// hold-music playback, so it can't break it itself. A plain uuid_break (NOT
// "all", which was part of the earlier connect regression). A break happens at
// most once per dequeue, so the per-break dial is not a hot path.
func (e *Executor) breakChannel(channelUUID string) {
	if e.eslAddr == "" {
		e.Logger.Warn("queue: no ESL addr configured, cannot break hold music")
		return
	}
	c, err := eslgo.Dial(e.eslAddr, e.eslPassword, func() {})
	if err != nil {
		e.Logger.Warn("queue: esl dial for uuid_break failed", "err", err)
		return
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = c.SendCommand(ctx, command.API{Command: "uuid_break", Arguments: channelUUID})
}

// teamConfig is the queue-relevant settings of a team.
type teamConfig struct {
	name        string
	strategy    string
	ringSeconds int
	maxWait     int
	moh         string
}

// loadTeamConfig fetches a team's queue settings and resolves its effective
// hold music; ok=false when the team doesn't exist.
func (e *Executor) loadTeamConfig(ctx context.Context, tenantID uuid.UUID, team string) (teamConfig, bool, error) {
	var (
		cfg       teamConfig
		holdMusic string // the tenant's system hold music (subselected by get-team-moh)
	)
	err := e.DB.Pool.QueryRow(ctx, e.Q.GetTeamMOH, tenantID, team).Scan(
		&cfg.strategy, &cfg.ringSeconds, &cfg.maxWait, &cfg.moh, &holdMusic)
	if errors.Is(err, pgx.ErrNoRows) {
		return teamConfig{}, false, nil
	}
	if err != nil {
		return teamConfig{}, false, err
	}
	cfg.name = team
	cfg.moh = effectiveMOH(cfg.moh, holdMusic)
	return cfg, true, nil
}

// effectiveMOH resolves the hold music a team plays: an explicit per-team
// override wins; otherwise the tenant's uploaded system hold music; otherwise
// the built-in default stream. teams.moh_sound defaults to 'local_stream://moh'
// in the schema, so that value (or empty) means "no team override".
func effectiveMOH(teamMOH, tenantHoldMusic string) string {
	const builtin = "local_stream://moh"
	if teamMOH != "" && teamMOH != builtin {
		return teamMOH
	}
	if tenantHoldMusic != "" {
		return tenantHoldMusic
	}
	return builtin
}

// teamMemberSips returns a team's member SIP usernames in position order
// (available_for_calls only), regardless of live presence. Empty = no members.
func (e *Executor) teamMemberSips(ctx context.Context, tenantID uuid.UUID, team string) ([]string, error) {
	rows, err := e.DB.Pool.Query(ctx, e.Q.ListTeamAgentSips, tenantID, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sips []string
	for rows.Next() {
		var sip string
		var pos int
		if err := rows.Scan(&sip, &pos); err != nil {
			return nil, err
		}
		sips = append(sips, sip)
	}
	return sips, rows.Err()
}

// buildTeamDial orders the given (available) members per strategy and joins
// them into a bridge dial-string: ',' (all ring at once) for ring_all, '|'
// (ring one at a time, fall through on no-answer) for the ordered strategies,
// bounded per attempt by originate_timeout.
func (e *Executor) buildTeamDial(ctx context.Context, tenantID uuid.UUID, team, strategy string, ringSeconds int, sips []string, domain, label string) string {
	sips = e.orderByStrategy(ctx, tenantID, team, strategy, sips)
	targets := e.buildAgentLegs(sips, domain, tenantID.String(), label)
	sep := ","
	if strategy != "" && strategy != "ring_all" {
		sep = "|"
	}
	dial := strings.Join(targets, sep)
	if ringSeconds > 0 {
		dial = fmt.Sprintf("{originate_timeout=%d}%s", ringSeconds, dial)
	}
	return dial
}

// channelAlive reports whether the channel still exists (uuid_exists). Used to
// detect a caller who hung up while queued so the wait loop exits promptly.
func channelAlive(ctx context.Context, conn *eslgo.Conn, channelUUID string) bool {
	resp, err := conn.SendCommand(ctx, command.API{Command: "uuid_exists", Arguments: channelUUID})
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(resp.Body)) == "true"
}

// ─── http_callback node ─────────────────────────────────────────────

// httpRespBodyCap bounds how much of a callback response we read, so a huge or
// hostile body can't exhaust memory.
const httpRespBodyCap = 1 << 20 // 1 MiB

// runHTTPCallback calls the integrator's backend so the flow can route on data
// only they have (who is calling → which team/queue/priority). It templates
// {{vars}} into the URL + body, performs the request, and on a 2xx stashes the
// top-level JSON fields into both flow vars (for downstream nodes like Transfer)
// and channel vars (tring_<key>, visible to the CDR). Branches http:2xx /
// http:non2xx; never errors the call (a backend problem routes to the error
// edge, it doesn't drop the caller).
func (e *Executor) runHTTPCallback(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	tv := st.templateVars()
	url := substituteVars(stringFromConfig(node.Config, "url"), tv)
	method := strings.ToUpper(stringFromConfig(node.Config, "method"))
	if method == "" {
		method = "GET"
	}
	st.stepAction, st.stepDetail = "http_call", method+" "+url
	if url == "" {
		st.log.Warn("ivr http_callback has no url, routing non2xx", "node", node.ID)
		return "http:non2xx", nil
	}

	timeout := time.Duration(intFromConfig(node.Config, "timeout_seconds", 10)) * time.Second
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	bodyTmpl := stringFromConfig(node.Config, "body_template")
	if bodyTmpl != "" {
		body = strings.NewReader(substituteVars(bodyTmpl, tv))
	}
	req, err := http.NewRequestWithContext(rctx, method, url, body)
	if err != nil {
		st.log.Error("ivr http_callback bad request", "node", node.ID, "err", err)
		return "http:non2xx", nil
	}
	if bodyTmpl != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range mapStringFromConfig(node.Config, "headers") {
		req.Header.Set(k, v)
	}

	resp, err := e.httpClient().Do(req)
	if err != nil {
		st.log.Warn("ivr http_callback request failed, routing non2xx", "node", node.ID, "err", err)
		return "http:non2xx", nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, httpRespBodyCap))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		st.log.Info("ivr http_callback non-2xx", "node", node.ID, "status", resp.StatusCode)
		return "http:non2xx", nil
	}
	e.stashResponse(ctx, conn, st, respBody)
	return "http:2xx", nil
}

// stashResponse writes each top-level key of a JSON-object response into flow
// vars (st.vars[key]) and a channel var (tring_<key>), so later nodes can route
// on backend data. A non-object body is ignored (best-effort).
func (e *Executor) stashResponse(ctx context.Context, conn *eslgo.Conn, st *callState, body []byte) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		st.log.Info("ivr http_callback response not a JSON object, no vars stashed")
		return
	}
	for k, v := range obj {
		val := stringifyJSON(v)
		st.vars[k] = val
		if conn != nil { // nil in unit tests; live calls always have a channel
			_, _ = conn.SendCommand(ctx, &call.Set{UUID: st.uuid, Key: "tring_" + k, Value: val, Sync: false})
		}
	}
}

// substituteVars replaces {{key}} tokens in a template with vars[key]. Unknown
// tokens are left as-is (visible in logs so the author can spot a typo).
func substituteVars(tmpl string, vars map[string]string) string {
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", v)
	}
	return tmpl
}

// stringifyJSON renders a JSON value as the plain string stored in a flow/channel
// var (so {"priority":true} → "true", {"team":"sales"} → "sales").
func stringifyJSON(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// answeredAgentLabel returns the display label of the agent attributed to this
// call (stamped by the CHANNEL_BRIDGE handler), or "" if none answered. Uses a
// fresh short context so it works even after the call's ctx is cancelled.
func (e *Executor) answeredAgentLabel(fsUUID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var label string
	if err := e.DB.Pool.QueryRow(ctx, e.Q.GetCallAgentLabel, fsUUID).Scan(&label); err != nil {
		return ""
	}
	return label
}

// agentBridgeTarget builds the bridge dial-string for a Transfer node. If the
// node names a specific agent (config.agent_sip), ring just that one;
// otherwise ring ALL of the tenant's agents simultaneously (comma-joined —
// first to answer wins). Unregistered agents simply fail their leg, so no
// presence filtering is needed. Returns ("", nil, nil) when the tenant has no
// agents. The second return value is the candidate SIP usernames, in the same
// order as the dial-string legs — used after the bridge to attribute which
// agent actually answered.
func (e *Executor) agentBridgeTarget(ctx context.Context, node *ivrNode, tenantID uuid.UUID, label string) (string, []string, error) {
	var domain string
	if err := e.DB.Pool.QueryRow(ctx, e.Q.GetTenantSipDomain, tenantID).Scan(&domain); err != nil {
		return "", nil, err
	}

	// Legacy single-agent pin — ring exactly that agent.
	if sip := stringFromConfig(node.Config, "agent_sip"); sip != "" {
		return e.agentLeg(sip, domain, tenantID.String(), label), []string{sip}, nil
	}

	// Note: team targets are handled in runTeamTransfer (queue-aware) before
	// reaching here, so this path is only the legacy ring-all fallback.

	// Back-compat: no agent and no team → ring ALL tenant agents simultaneously
	// (the pre-teams behaviour).
	rows, err := e.DB.Pool.Query(ctx, e.Q.ListAgentSipUsernames, tenantID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var sips []string
	for rows.Next() {
		var sip string
		if err := rows.Scan(&sip); err != nil {
			return "", nil, err
		}
		sips = append(sips, sip)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	sips = e.filterAvailable(ctx, tenantID, sips)
	targets := e.buildAgentLegs(sips, domain, tenantID.String(), label)
	return strings.Join(targets, ","), sips, nil // comma = simultaneous ring
}

// filterAvailable keeps only agents connected to the gateway and not already on
// a call. With the legacy SIP transport or when presence is off it returns the
// input unchanged (FreeSWITCH fails dead legs itself).
func (e *Executor) filterAvailable(ctx context.Context, tenantID uuid.UUID, sips []string) []string {
	pres := presence.New(e.Cache)
	if e.AgentTransport != "gateway" || !pres.Enabled() {
		return sips
	}
	// Fresh slice — do NOT reuse sips' backing array (sips[:0]), which would
	// corrupt the caller's member list across queue-poll iterations.
	avail := make([]string, 0, len(sips))
	for _, sip := range sips {
		// Skip agents another caller is already ringing (reserved) — presence
		// won't show them busy until they answer, so this prevents a double-pick.
		if e.reserve.reserved(ctx, resKey(tenantID, sip)) {
			continue
		}
		if pres.Available(ctx, tenantID.String(), sip) {
			avail = append(avail, sip)
		}
	}
	return avail
}

// buildAgentLegs maps each sip to a bridge dial-string leg (see agentLeg).
func (e *Executor) buildAgentLegs(sips []string, domain, tenant, label string) []string {
	legs := make([]string, 0, len(sips))
	for _, sip := range sips {
		legs = append(legs, e.agentLeg(sip, domain, tenant, label))
	}
	return legs
}

// reserveAgents marks the given agents as being rung (so a concurrent dequeue —
// within or, when Redis-backed, across orchestrators — queues instead of
// double-picking them) and returns a cleanup that releases them. The release
// uses a fresh background context so it still runs after the (Sync) bridge, when
// the call context may already be done; the TTL is the backstop if it doesn't.
func (e *Executor) reserveAgents(ctx context.Context, tenantID uuid.UUID, sips []string, ttl time.Duration) func() {
	for _, sip := range sips {
		e.reserve.reserve(ctx, resKey(tenantID, sip), ttl)
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for _, sip := range sips {
			e.reserve.release(rctx, resKey(tenantID, sip))
		}
	}
}

// orderByStrategy reorders a team's available members for ringing:
//   - round_robin: rotate the start by a per-team counter (load spreading)
//   - longest_wait: idle-longest first (by Redis freed-at; missing = longest)
//   - sequential / ring_all: keep membership-position order
func (e *Executor) orderByStrategy(ctx context.Context, tenantID uuid.UUID, team, strategy string, sips []string) []string {
	switch strategy {
	case "round_robin":
		if len(sips) > 1 {
			start := e.rrNext(tenantID.String()+"/"+team, len(sips))
			sips = append(append([]string{}, sips[start:]...), sips[:start]...)
		}
	case "longest_wait":
		pres := presence.New(e.Cache)
		if pres.Enabled() {
			freed := make(map[string]time.Time, len(sips))
			for _, sip := range sips {
				if t, ok := pres.FreedAt(ctx, tenantID.String(), sip); ok {
					freed[sip] = t
				} // absent → zero time → sorts first (idle longest)
			}
			sort.SliceStable(sips, func(i, j int) bool {
				return freed[sips[i]].Before(freed[sips[j]])
			})
		}
	}
	return sips
}

// rrNext returns the next rotation offset for a round_robin key (0..n-1) and
// advances the per-team counter. Bounded so the counter never overflows.
func (e *Executor) rrNext(key string, n int) int {
	e.rrMu.Lock()
	defer e.rrMu.Unlock()
	if e.rr == nil {
		e.rr = map[string]int{}
	}
	i := e.rr[key] % n
	e.rr[key] = (e.rr[key] + 1) % (1 << 30)
	return i
}

// agentLeg builds one bridge dial-string leg for an agent via the shared
// agentleg helper. Attribute is always set so the CHANNEL_BRIDGE handler can
// read the tring_agent variable off the answered B-leg and attribute the call —
// reliable even for a ring-all where we otherwise wouldn't know which leg
// picked up. Inbound transfers ring the agent for a manual accept, so
// AutoAnswer stays off (that's the API click-to-call path).
func (e *Executor) agentLeg(sip, domain, tenantID, label string) string {
	return agentleg.Build(agentleg.Options{
		Transport: e.AgentTransport,
		Gateway:   e.AgentGateway,
		SIP:       sip,
		Domain:    domain,
		TenantID:  tenantID,
		Label:     label,
		Attribute: true,
	})
}

func (e *Executor) runHangup(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	st.stepAction = "hangup"
	// Optional goodbye audio before hangup.
	if file, ok := node.Config["audio_file"].(string); ok && file != "" {
		_, _ = conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "playback", AppArgs: file, Sync: true})
	}
	_ = conn.HangupCall(ctx, st.uuid, "NORMAL_CLEARING")
	return "", nil // terminal: no outgoing edge
}

// ─── Helpers ────────────────────────────────────────────────────────

func intFromConfig(c map[string]interface{}, key string, def int) int {
	if v, ok := c[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

func stringFromConfig(c map[string]interface{}, key string) string {
	if v, ok := c[key].(string); ok {
		return v
	}
	return ""
}

func boolFromConfig(c map[string]interface{}, key string) bool {
	if v, ok := c[key].(bool); ok {
		return v
	}
	return false
}

func mapFromConfig(c map[string]interface{}, key string) map[string]interface{} {
	if v, ok := c[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

// mapStringFromConfig reads a JSON object config field as map[string]string,
// dropping non-string values (used for http_callback headers).
func mapStringFromConfig(c map[string]interface{}, key string) map[string]string {
	out := map[string]string{}
	if m, ok := c[key].(map[string]interface{}); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

// getChannelVar reads ${var} off the live channel via `uuid_getvar`.
// FreeSWITCH responds with the raw value in the body; we trim it.
func getChannelVar(ctx context.Context, conn *eslgo.Conn, channelUUID, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := conn.SendCommand(ctx, command.API{
		Command:   "uuid_getvar",
		Arguments: channelUUID + " " + name,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(resp.Body)), nil
}
