// IVR simulator — walks a flow without an ESL connection and returns
// a structured trace of what would happen on a real call.
//
// Used by POST /v1/flows/{id}/simulate (api) to give flow authors
// instant feedback in the editor: "I picked '1' on the welcome menu,
// where would the call go?" Nodes that would normally hit FreeSWITCH
// (greeting playback, menu prompt, transfer bridge) are recorded as
// `Step` entries with action + detail; no FS round trip happens.
//
// Determinism: the simulator never reads wall-clock time directly —
// pass SimInput.Now to drive Timing nodes. DTMF inputs are a queue; one
// digit is consumed per menu/gather. Empty queue → timeout branch.

package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SimInput is everything the operator can pre-stage to drive the
// simulation deterministically.
type SimInput struct {
	// Caller defaults to "+10000000000" if empty. Surfaced to the
	// trace so flow authors can read what the simulated channel sees.
	Caller string `json:"caller,omitempty"`

	// DTMF is a FIFO of digits consumed by Menu/Gather nodes in order.
	// When a Menu pops a digit that doesn't match any option, the
	// branch follows "max_retries"; when the queue is empty, "timeout".
	DTMF []string `json:"dtmf,omitempty"`

	// Vars pre-populates the call state so a Gather earlier in the
	// flow can be skipped while still testing downstream branches.
	Vars map[string]string `json:"vars,omitempty"`

	// Now overrides the current time for Timing nodes. Zero value =
	// time.Now() (so a quick sim without a fixture still works).
	Now time.Time `json:"now,omitempty"`
}

// SimStep is one row in the trace.
type SimStep struct {
	NodeID    string `json:"node_id"`
	NodeType  string `json:"node_type"`
	Action    string `json:"action"`             // play_audio, speak, prompt_dtmf, transfer, hangup, branch, passthrough, http_call
	Detail    string `json:"detail,omitempty"`   // file path, agent number, URL, etc.
	DTMFInput string `json:"dtmf_input,omitempty"` // digit consumed at a menu/gather, if any
	Outcome   string `json:"outcome"`            // edge handle id taken: "default", "digit:1", "timeout"...
}

// SimResult is the full trace returned to the caller.
type SimResult struct {
	Steps       []SimStep         `json:"steps"`
	EndedAt     string            `json:"ended_at"`
	HangupCause string            `json:"hangup_cause"`
	Vars        map[string]string `json:"vars"`
	// Error is populated when the simulator hit something it couldn't
	// reasonably continue past (missing node, edge missing for outcome,
	// max-depth loop). The caller still gets the partial Steps so the
	// flow author can see how far the call got.
	Error string `json:"error,omitempty"`
}

// SimulateFlow parses the flow definition JSON (numbers.flow_id →
// flows.definition) and walks it with the supplied input. Errors only
// happen for un-parseable JSON or a missing entry_node; a runtime walk
// problem populates SimResult.Error and still returns the partial trace.
func SimulateFlow(defJSON []byte, in SimInput) (*SimResult, error) {
	var flow ivrFlowData
	if err := json.Unmarshal(defJSON, &flow); err != nil {
		return nil, fmt.Errorf("parse flow: %w", err)
	}
	if flow.EntryNode == "" {
		return nil, fmt.Errorf("flow has no entry node")
	}
	return simulate(&flow, in), nil
}

func simulate(flow *ivrFlowData, in SimInput) *SimResult {
	res := &SimResult{
		Steps: []SimStep{},
		Vars:  map[string]string{},
	}
	for k, v := range in.Vars {
		res.Vars[k] = v
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if in.Caller == "" {
		in.Caller = "+10000000000"
	}

	dtmfIdx := 0
	nodeID := flow.EntryNode
	for depth := 0; depth < 64; depth++ {
		node, ok := findNode(flow, nodeID)
		if !ok {
			res.Error = fmt.Sprintf("node %q not found", nodeID)
			return res
		}
		step, outcome := stepFor(node, &in, &dtmfIdx, res.Vars)
		res.Steps = append(res.Steps, step)

		if outcome == "" {
			// Terminal node (hangup, or executor would have ended).
			res.EndedAt = node.ID
			if step.NodeType == nodeHangup {
				res.HangupCause = "NORMAL_CLEARING"
			} else {
				res.HangupCause = "END_OF_FLOW"
			}
			return res
		}
		next, ok := nextNode(flow, node.ID, outcome)
		if !ok {
			res.Error = fmt.Sprintf("node %s has no edge for outcome %q", node.ID, outcome)
			res.EndedAt = node.ID
			res.HangupCause = "TEMPORARY_FAILURE"
			return res
		}
		nodeID = next
	}
	res.Error = "exceeded max walk depth (64)"
	return res
}

// stepFor records what a node would do and returns the branch the
// simulator should follow. Pure — no FS, no DB, no clock except via
// the passed-in time.
func stepFor(node *ivrNode, in *SimInput, dtmfIdx *int, vars map[string]string) (SimStep, string) {
	step := SimStep{NodeID: node.ID, NodeType: node.Type}

	switch node.Type {
	case nodeStart:
		step.Action = "passthrough"
		step.Outcome = "default"
		return step, "default"

	case nodeGreeting:
		if f, ok := node.Config["audio_file"].(string); ok && f != "" {
			step.Action = "play_audio"
			step.Detail = f
		} else if t, ok := node.Config["greeting_text"].(string); ok && t != "" {
			step.Action = "speak"
			step.Detail = t
		} else {
			step.Action = "play_audio"
			step.Detail = "(no audio configured)"
		}
		step.Outcome = "default"
		return step, "default"

	case nodeMenu:
		step.Action = "prompt_dtmf"
		if t, ok := node.Config["greeting_text"].(string); ok && t != "" {
			step.Detail = t
		} else if f, ok := node.Config["audio_file"].(string); ok && f != "" {
			step.Detail = f
		}
		opts := mapFromConfig(node.Config, "options")
		if *dtmfIdx >= len(in.DTMF) {
			step.Outcome = "timeout"
			return step, "timeout"
		}
		digit := in.DTMF[*dtmfIdx]
		*dtmfIdx++
		step.DTMFInput = digit
		if _, ok := opts[digit]; ok {
			step.Outcome = "digit:" + digit
			return step, "digit:" + digit
		}
		// Operator pressed something not in the menu — production would
		// retry max_retries times then take that branch. Simulator
		// short-circuits to the same final branch for honesty.
		step.Outcome = "max_retries"
		return step, "max_retries"

	case nodeGather:
		step.Action = "prompt_dtmf"
		if t, ok := node.Config["greeting_text"].(string); ok && t != "" {
			step.Detail = t
		}
		maxDigits := intFromConfig(node.Config, "max_digits", 10)
		gathered := ""
		for *dtmfIdx < len(in.DTMF) && len(gathered) < maxDigits {
			gathered += in.DTMF[*dtmfIdx]
			*dtmfIdx++
		}
		if gathered == "" {
			step.Outcome = "timeout"
			return step, "timeout"
		}
		step.DTMFInput = gathered
		if storeAs, ok := node.Config["store_as"].(string); ok && storeAs != "" {
			vars[storeAs] = gathered
		}
		step.Outcome = "default"
		return step, "default"

	case nodeHTTPCallback:
		step.Action = "http_call"
		url, _ := node.Config["url"].(string)
		method, _ := node.Config["method"].(string)
		if method == "" {
			method = "GET"
		}
		step.Detail = method + " " + url
		// No real request — assume happy path. A future mocking surface
		// could let the operator choose "http:non2xx" here.
		step.Outcome = "http:2xx"
		return step, "http:2xx"

	case nodeTransfer:
		step.Action = "transfer"
		agent, _ := node.Config["agent_number"].(string)
		step.Detail = agent
		step.Outcome = "completed"
		return step, "completed"

	case nodeGotoFlow:
		step.Action = "branch"
		flowID, _ := node.Config["flow_id"].(string)
		step.Detail = "would jump to flow " + flowID
		// Simulator doesn't recurse into linked flows — it ends here
		// with a clear note so the operator knows to test the target
		// flow separately.
		step.Outcome = ""
		return step, ""

	case nodeTiming:
		step.Action = "branch"
		// Use the simulator-supplied clock so tests are deterministic.
		hour := in.Now.Hour()
		// Simplified: 9–18 local time = in hours, else closed. Phase 3
		// will add per-tenant schedules; mirror that logic here when it
		// lands.
		if hour >= 9 && hour < 18 {
			step.Outcome = "in_hours"
			step.Detail = fmt.Sprintf("now=%s → open", in.Now.Format(time.RFC3339))
		} else {
			step.Outcome = "out_of_hours"
			step.Detail = fmt.Sprintf("now=%s → closed", in.Now.Format(time.RFC3339))
		}
		return step, step.Outcome

	case nodeHangup:
		step.Action = "hangup"
		step.Outcome = ""
		return step, ""

	default:
		step.Action = "unknown"
		step.Detail = "unknown node type"
		step.Outcome = ""
		return step, ""
	}
}

// nextNode resolves the next node id by matching an outgoing edge whose
// condition equals the outcome. Returns ok=false if no edge matches
// (flow author's bug — operator sees it in the trace).
func nextNode(flow *ivrFlowData, from, outcome string) (string, bool) {
	for _, e := range flow.Edges {
		if e.From == from && e.Condition == outcome {
			return e.To, true
		}
	}
	// Fall back: if no exact match but there's a "default" edge, take
	// that. Matches real-executor behavior for nodes that only define
	// the happy branch.
	if outcome != "default" {
		for _, e := range flow.Edges {
			if e.From == from && e.Condition == "default" {
				return e.To, true
			}
		}
	}
	return "", false
}

// mapFromConfig / intFromConfig are shared with ivr_executor.go in
// this package, so no duplicate helper is needed here.

// _ = strings.ToLower keeps the import wired in case a digit-
// normalisation pass lands here in a follow-up (# / * handling, etc.).
var _ = strings.ToLower
