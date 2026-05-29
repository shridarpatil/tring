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
	Logger *log.Logger
}

func NewExecutor(d *db.DB) *Executor {
	return &Executor{DB: d, Logger: log.Default()}
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

	if uuidStr == "" || called == "" {
		e.Logger.Printf("ivr: missing UUID or destination on inbound call")
		_ = conn.HangupCall(ctx, uuidStr, "CALL_REJECTED")
		return
	}

	// Look up the flow for this DID. Schema: numbers.flow_id → flows.id.
	// Tenant scoping comes through numbers.tenant_id.
	var (
		tenantID uuid.UUID
		flowID   *uuid.UUID
		defJSON  []byte
	)
	row := e.DB.Pool.QueryRow(ctx, `
		SELECT n.tenant_id, n.flow_id, COALESCE(f.definition::text, '')
		FROM numbers n
		LEFT JOIN flows f ON f.id = n.flow_id AND f.is_active
		WHERE n.e164 = $1 AND n.type = 'inbound'
	`, called)
	if err := row.Scan(&tenantID, &flowID, &defJSON); err != nil {
		e.Logger.Printf("ivr: no flow for %s: %v", called, err)
		_ = conn.HangupCall(ctx, uuidStr, "UNALLOCATED_NUMBER")
		return
	}
	if flowID == nil || len(defJSON) == 0 {
		e.Logger.Printf("ivr: %s has no flow attached; hanging up", called)
		_ = conn.HangupCall(ctx, uuidStr, "NORMAL_CLEARING")
		return
	}

	var flow ivrFlowData
	if err := json.Unmarshal(defJSON, &flow); err != nil {
		e.Logger.Printf("ivr: bad flow JSON for %s: %v", called, err)
		_ = conn.HangupCall(ctx, uuidStr, "TEMPORARY_FAILURE")
		return
	}
	if flow.EntryNode == "" {
		e.Logger.Printf("ivr: flow %s has no entry node", *flowID)
		_ = conn.HangupCall(ctx, uuidStr, "TEMPORARY_FAILURE")
		return
	}

	if err := conn.AnswerCall(ctx, uuidStr); err != nil {
		e.Logger.Printf("ivr: answer failed: %v", err)
		return
	}

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

		outcome, err := e.runNode(ctx, conn, node, st)
		if err != nil {
			return fmt.Errorf("node %s (%s): %w", node.ID, node.Type, err)
		}
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
	if file, ok := node.Config["audio_file"].(string); ok && file != "" {
		_, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "playback", AppArgs: file, Sync: true})
		if err != nil {
			return "", fmt.Errorf("playback: %w", err)
		}
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
	agent := stringFromConfig(node.Config, "agent_number")
	if agent == "" {
		return "no_answer", nil
	}
	// TODO: pick a trunk via selectTrunks(); hardcoded to loopback here.
	bridgeTo := fmt.Sprintf("loopback/%s", agent)
	if _, err := conn.SendCommand(ctx, &call.Execute{UUID: st.uuid, AppName: "bridge", AppArgs: bridgeTo, Sync: true}); err != nil {
		return "", fmt.Errorf("bridge: %w", err)
	}
	// Branch on bridge result via channel var ${last_bridge_hangup_cause}.
	cause, _ := getChannelVar(ctx, conn, st.uuid, "originate_disposition")
	if strings.Contains(cause, "ANSWER") || strings.Contains(cause, "SUCCESS") {
		return "completed", nil
	}
	return "no_answer", nil
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
