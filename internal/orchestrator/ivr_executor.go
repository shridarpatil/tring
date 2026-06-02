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
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"
	"github.com/percipia/eslgo/command/call"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/queries"
)

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
}

func NewExecutor(d *db.DB) *Executor {
	return &Executor{DB: d, Q: queries.MustLoad(), Logger: log.Default()}
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

	// Single-use guest credentials: when this call ends, expire the guest
	// session so the minted SIP credential can't be replayed for another
	// call. (Set by the mod_xml_curl directory for guest_* users.)
	if gu := ev.GetHeader("variable_tring_guest_session"); gu != "" {
		defer e.expireGuestSession(gu)
	}

	// Two ways a flow gets resolved:
	//   - guest web-call: the flow is pinned by the tring_flow_id channel
	//     variable (set in the mod_xml_curl directory for guest_* users),
	//     so there's no dialed DID to look up.
	//   - PSTN/DID call: discover the flow from the dialed inbound number.
	var resolved *ResolvedFlow
	var err error
	if fid := ev.GetHeader("variable_tring_flow_id"); fid != "" {
		e.Logger.Printf("ivr: guest call, flow pinned to %s", fid)
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

	e.Logger.Printf("ivr: resolved flow for %s, answering", called)
	if err := conn.AnswerCall(ctx, uuidStr); err != nil {
		e.Logger.Printf("ivr: answer failed: %v", err)
		return
	}
	e.Logger.Printf("ivr: answered, walking from entry node %s", flow.EntryNode)

	state := &callState{
		uuid:     uuidStr,
		tenantID: tenantID,
		caller:   caller,
		called:   called,
		vars:     map[string]string{},
	}

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
		defJSON  []byte
	)
	row := e.DB.Pool.QueryRow(ctx, e.Q.ResolveInboundFlow, called)
	if err := row.Scan(&tenantID, &flowID, &defJSON); err != nil {
		return nil, err
	}
	if flowID == nil || len(defJSON) == 0 {
		return &ResolvedFlow{TenantID: tenantID}, nil
	}
	var flow ivrFlowData
	if err := json.Unmarshal(defJSON, &flow); err != nil {
		return nil, fmt.Errorf("bad flow JSON: %w", err)
	}
	if flow.EntryNode == "" {
		return nil, fmt.Errorf("flow has no entry node")
	}
	return &ResolvedFlow{TenantID: tenantID, Flow: &flow}, nil
}

// expireGuestSession deletes a guest_sessions row once its single call has
// ended, enforcing single-use credentials. Uses a fresh short context
// because the call's context is already cancelled when the call ends.
func (e *Executor) expireGuestSession(sipUser string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tag, err := e.DB.Pool.Exec(ctx, e.Q.ExpireGuestSession, sipUser)
	if err != nil {
		e.Logger.Printf("ivr: failed to expire guest session %s: %v", sipUser, err)
		return
	}
	if tag.RowsAffected() > 0 {
		e.Logger.Printf("ivr: expired single-use guest session %s", sipUser)
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
	var (
		tenantID uuid.UUID
		defJSON  []byte
	)
	row := e.DB.Pool.QueryRow(ctx, e.Q.GetActiveFlowByID, fid)
	if err := row.Scan(&tenantID, &defJSON); err != nil {
		return nil, err
	}
	if len(defJSON) == 0 {
		return &ResolvedFlow{TenantID: tenantID}, nil
	}
	var flow ivrFlowData
	if err := json.Unmarshal(defJSON, &flow); err != nil {
		return nil, fmt.Errorf("bad flow JSON: %w", err)
	}
	if flow.EntryNode == "" {
		return nil, fmt.Errorf("flow has no entry node")
	}
	return &ResolvedFlow{TenantID: tenantID, Flow: &flow}, nil
}

// callState is the per-call mutable state — variables gathered via
// Gather nodes, the caller/callee identifiers, and tenant context.
type callState struct {
	uuid     string
	tenantID uuid.UUID
	caller   string
	called   string
	vars     map[string]string
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

		outcome, err := e.runNode(ctx, conn, node, st)
		if err != nil {
			return fmt.Errorf("node %s (%s): %w", node.ID, node.Type, err)
		}
		e.Logger.Printf("ivr: walk node=%s outcome=%q", node.ID, outcome)
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
		e.Logger.Printf("ivr: playback file=%s (sync)", file)
		_, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "playback", AppArgs: file, Sync: true})
		if err != nil {
			return "", fmt.Errorf("playback: %w", err)
		}
		e.Logger.Printf("ivr: playback done")
	} else if text, ok := node.Config["greeting_text"].(string); ok && text != "" {
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

	digit, err := getChannelVar(ctx, conn, st.uuid, "ivr_choice")
	if err != nil || digit == "" {
		return "timeout", nil
	}
	if _, ok := options[digit]; !ok {
		return "max_retries", nil
	}
	return "digit:" + digit, nil
}

func (e *Executor) runTransfer(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
	bridgeTo, err := e.agentBridgeTarget(ctx, node, st.tenantID)
	if err != nil {
		e.Logger.Printf("ivr: transfer target lookup failed: %v", err)
		return "no_answer", nil
	}
	if bridgeTo == "" {
		e.Logger.Printf("ivr: transfer node has no agents to ring (tenant=%s)", st.tenantID)
		return "no_answer", nil
	}
	e.Logger.Printf("ivr: transfer bridging to %s", bridgeTo)

	// Label the caller so the agent's incoming-call screen shows something
	// meaningful (web visitors have no real caller-id).
	_, _ = conn.SendCommand(ctx, &call.Set{UUID: st.uuid, Key: "effective_caller_id_name", Value: "Web Caller", Sync: true})

	if _, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "bridge", AppArgs: bridgeTo, Sync: true}); err != nil {
		return "", fmt.Errorf("bridge: %w", err)
	}
	// Branch on bridge result via channel var ${originate_disposition}.
	cause, _ := getChannelVar(ctx, conn, st.uuid, "originate_disposition")
	if strings.Contains(cause, "ANSWER") || strings.Contains(cause, "SUCCESS") {
		return "completed", nil
	}
	return "no_answer", nil
}

// agentBridgeTarget builds the bridge dial-string for a Transfer node. If the
// node names a specific agent (config.agent_sip), ring just that one;
// otherwise ring ALL of the tenant's agents simultaneously (comma-joined —
// first to answer wins). Unregistered agents simply fail their leg, so no
// presence filtering is needed. Returns "" when the tenant has no agents.
func (e *Executor) agentBridgeTarget(ctx context.Context, node *ivrNode, tenantID uuid.UUID) (string, error) {
	var domain string
	if err := e.DB.Pool.QueryRow(ctx, e.Q.GetTenantSipDomain, tenantID).Scan(&domain); err != nil {
		return "", err
	}

	if sip := stringFromConfig(node.Config, "agent_sip"); sip != "" {
		return fmt.Sprintf("user/%s@%s", sip, domain), nil
	}

	rows, err := e.DB.Pool.Query(ctx, e.Q.ListAgentSipUsernames, tenantID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var sip string
		if err := rows.Scan(&sip); err != nil {
			return "", err
		}
		targets = append(targets, fmt.Sprintf("user/%s@%s", sip, domain))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(targets, ","), nil // comma = simultaneous ring
}

func (e *Executor) runHangup(ctx context.Context, conn *eslgo.Conn, node *ivrNode, st *callState) (string, error) {
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
