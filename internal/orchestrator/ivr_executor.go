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
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
	"github.com/percipia/eslgo/command/call"

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
	Logger *log.Logger
	// Cache holds resolved flow definitions (cache-aside, 1h TTL). May be
	// nil — caching disabled, every resolve reads the DB. Set via WithCache.
	Cache *cache.Cache
	// AgentTransport / AgentGateway mirror the API: how an agent's bridge leg
	// is routed ("sip" → user/<sip>; "gateway" → the media gateway as a SIP
	// endpoint, ADR-001 M4b). Set via WithAgentTransport. Empty = "sip".
	AgentTransport string
	AgentGateway   string
}

func NewExecutor(d *db.DB) *Executor {
	return &Executor{DB: d, Q: queries.MustLoad(), Logger: log.Default()}
}

// WithCache attaches a Redis cache for flow definitions and returns the
// executor for chaining. Passing a nil cache leaves caching disabled.
func (e *Executor) WithCache(c *cache.Cache) *Executor {
	e.Cache = c
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
	e.Logger.Printf("ivr: handler entry uuid=%s called=%s caller=%s", uuidStr, called, caller)

	if uuidStr == "" || called == "" {
		e.Logger.Printf("ivr: missing UUID or destination on inbound call")
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
		e.Logger.Printf("ivr: web call, flow pinned to %s", fid)
		resolved, err = e.resolveFlowByID(ctx, fid)
	} else {
		resolved, err = e.resolveFlow(ctx, called)
	}
	if err != nil {
		e.Logger.Printf("ivr: no flow for %s: %v", called, err)
		_ = conn.HangupCall(ctx, uuidStr, "UNALLOCATED_NUMBER")
		return
	}
	if resolved.Flow == nil {
		e.Logger.Printf("ivr: %s has no flow attached; hanging up", called)
		_ = conn.HangupCall(ctx, uuidStr, "NORMAL_CLEARING")
		return
	}
	tenantID := resolved.TenantID
	flow := *resolved.Flow

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

	e.Logger.Printf("ivr: resolved flow for %s, answering", called)
	if err := conn.AnswerCall(ctx, uuidStr); err != nil {
		e.Logger.Printf("ivr: answer failed: %v", err)
		return
	}
	e.Logger.Printf("ivr: answered, walking from entry node %s", flow.EntryNode)

	if err := e.walk(ctx, conn, &flow, flow.EntryNode, state); err != nil {
		e.Logger.Printf("ivr: walk error: %v", err)
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
		e.Logger.Printf("ivr: failed to record inbound call (uuid=%s): %v", st.uuid, err)
		return
	}
	e.Logger.Printf("ivr: recorded inbound call %s (uuid=%s)", id, st.uuid)
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
		e.Logger.Printf("ivr: marshal ivr_path (uuid=%s): %v", st.uuid, err)
		return
	}
	if _, err := e.DB.Pool.Exec(ctx, e.Q.SetCallIVRPath, string(b), st.uuid); err != nil {
		e.Logger.Printf("ivr: save ivr_path (uuid=%s): %v", st.uuid, err)
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
		e.Logger.Printf("ivr: walk depth=%d node=%s type=%s", depth, node.ID, node.Type)

		// Clear per-node scratch; the handler fills it in, then we record
		// one step from it below (after the outcome is known).
		st.stepAction, st.stepDetail, st.stepDTMF = "", "", ""
		outcome, err := e.runNode(ctx, conn, node, st)
		if err != nil {
			st.recordStep(node, "error")
			return fmt.Errorf("node %s (%s): %w", node.ID, node.Type, err)
		}
		e.Logger.Printf("ivr: walk node=%s outcome=%q", node.ID, outcome)
		st.recordStep(node, outcome)
		if outcome == "" {
			// Terminal node (hangup) or executor explicitly ended.
			return nil
		}

		next := findNextNode(flow, nodeID, outcome)
		if next == "" {
			e.Logger.Printf("ivr: node %s outcome=%s has no outgoing edge — ending", node.ID, outcome)
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
		e.Logger.Printf("ivr: gather node not yet implemented; following default")
		return "default", nil
	case nodeHTTPCallback:
		// TODO: substitute {{vars}} in URL/body, HTTP request, branch on 2xx vs non2xx
		e.Logger.Printf("ivr: http_callback node not yet implemented; following http:2xx")
		return "http:2xx", nil
	case nodeTiming:
		// TODO: evaluate config.schedule against time.Now in tenant TZ → "in_hours" / "out_of_hours"
		e.Logger.Printf("ivr: timing node not yet implemented; following in_hours")
		return "in_hours", nil
	case nodeGotoFlow:
		// TODO: load flow_id, recurse into walk() against new flow.entry_node
		e.Logger.Printf("ivr: goto_flow node not yet implemented; ending call")
		return "", nil

	default:
		return "", fmt.Errorf("unknown node type %q", node.Type)
	}
}

// ─── Node implementations (real) ────────────────────────────────────

func (e *Executor) runGreeting(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	e.Logger.Printf("ivr: runGreeting node=%s config=%+v", node.ID, node.Config)
	if file, ok := node.Config["audio_file"].(string); ok && file != "" {
		st.stepAction, st.stepDetail = "play_audio", file
		e.Logger.Printf("ivr: playback file=%s (sync)", file)
		_, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "playback", AppArgs: file, Sync: true})
		if err != nil {
			return "", fmt.Errorf("playback: %w", err)
		}
		e.Logger.Printf("ivr: playback done")
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
	// Record the target for the call log: the specific agent if one is
	// pinned, otherwise "all agents" (ring-all). The outcome (completed /
	// no_answer) is filled in by walk from this function's return.
	st.stepAction = "transfer"
	if sip := stringFromConfig(node.Config, "agent_sip"); sip != "" {
		st.stepDetail = sip
	} else {
		st.stepDetail = "all agents"
	}

	// What the agent's incoming-call screen shows: the trusted client_ref if we
	// have one, else a generic label — never the gateway's internal SIP identity
	// (which is why agents saw "sipgo"). Carried as an X-Tring-Agent-Label SIP
	// header on the agent leg; the gateway reads it in preference to the From.
	label := st.clientRef
	if label == "" {
		label = "Web Caller"
	}
	bridgeTo, sips, err := e.agentBridgeTarget(ctx, node, st.tenantID, label)
	if err != nil {
		e.Logger.Printf("ivr: transfer target lookup failed: %v", err)
		return "no_answer", nil
	}
	if bridgeTo == "" {
		e.Logger.Printf("ivr: transfer node has no agents to ring (tenant=%s)", st.tenantID)
		return "no_answer", nil
	}
	e.Logger.Printf("ivr: transfer bridging to %s", bridgeTo)

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

	if sip := stringFromConfig(node.Config, "agent_sip"); sip != "" {
		return e.agentLeg(sip, domain, tenantID.String(), label), []string{sip}, nil
	}

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

	// With the gateway transport, an agent leg always reaches the gateway (it's
	// always up) and 480s if that agent isn't connected — so ringing offline
	// agents just wastes a leg. When presence is available, ring only agents
	// currently connected to the gateway AND not already on a call (Available);
	// an on-call agent should not be sent another call. (Presence disabled /
	// legacy SIP transport → ring all, as before; FS fails dead legs itself.)
	pres := presence.New(e.Cache)
	if e.AgentTransport == "gateway" && pres.Enabled() {
		avail := sips[:0]
		for _, sip := range sips {
			if pres.Available(ctx, tenantID.String(), sip) {
				avail = append(avail, sip)
			}
		}
		sips = avail
	}

	targets := make([]string, 0, len(sips))
	for _, sip := range sips {
		targets = append(targets, e.agentLeg(sip, domain, tenantID.String(), label))
	}
	return strings.Join(targets, ","), sips, nil // comma = simultaneous ring
}

// agentLeg builds one bridge dial-string leg for an agent, tagging it with a
// `tring_agent` channel variable. When the agent answers, the CHANNEL_BRIDGE
// event lets the orchestrator read this variable off the answered B-leg and
// attribute the call to that agent — reliable even for a ring-all where we
// otherwise wouldn't know which of N legs picked up.
//
// With the "gateway" transport the leg is routed to the media gateway as a SIP
// endpoint (carrying the tenant id as a SIP header) instead of user/<sip>, so
// agents speak WebRTC and never register SIP (ADR-001 M4b). Mirrors
// (*api.Server).agentALeg.
func (e *Executor) agentLeg(sip, domain, tenantID, label string) string {
	if e.AgentTransport == "gateway" {
		vars := fmt.Sprintf("tring_agent=%s,sip_h_X-Tring-Tenant=%s", sip, tenantID)
		// Agent-facing caller label (e.g. client_ref or "Web Caller"). The
		// gateway reads X-Tring-Agent-Label in preference to the SIP From, so the
		// agent never sees the gateway's internal "sipgo" identity.
		if l := sanitizeAgentLabel(label); l != "" {
			vars += ",sip_h_X-Tring-Agent-Label=" + l
		}
		return fmt.Sprintf("[%s]sofia/gateway/%s/%s", vars, e.AgentGateway, sip)
	}
	return fmt.Sprintf("[tring_agent=%s]user/%s@%s", sip, sip, domain)
}

// sanitizeAgentLabel strips characters that would break the FreeSWITCH
// `[k=v,...]` channel-variable block (or inject extra vars) and caps the length.
func sanitizeAgentLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == ',', r == '[', r == ']', r == '=', r == '\'', r == '"', r < 0x20:
			return -1
		}
		return r
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.TrimSpace(s)
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

func mapFromConfig(c map[string]interface{}, key string) map[string]interface{} {
	if v, ok := c[key].(map[string]interface{}); ok {
		return v
	}
	return nil
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
