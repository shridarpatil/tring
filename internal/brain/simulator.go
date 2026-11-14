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

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shridarpatil/tring/internal/ivr"
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

	// TransferOutcome forces a Transfer node's outcome so the author can test
	// the no_answer / queue_timeout branches (the simulator can't actually ring
	// or queue). Empty = "completed" (an agent answers).
	TransferOutcome string `json:"transfer_outcome,omitempty"`

	// HTTPOutcome forces an http_callback node's branch so the author can test
	// both paths — e.g. wiring the success branch to a priority Transfer and the
	// failure branch to a normal one. Empty/"http:2xx" = success; "http:non2xx".
	HTTPOutcome string `json:"http_outcome,omitempty"`
}

// transferOutcome returns the outcome a simulated Transfer node should take:
// the operator's forced TransferOutcome, validated, else "completed".
func transferOutcome(in SimInput) string {
	switch in.TransferOutcome {
	case "no_answer", "queue_timeout":
		return in.TransferOutcome
	default:
		return "completed"
	}
}

// httpOutcome returns the branch a simulated http_callback should take.
func httpOutcome(in SimInput) string {
	if in.HTTPOutcome == "http:non2xx" {
		return "http:non2xx"
	}
	return "http:2xx"
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

	// The same walker a live call uses (ivr.Walk): edge matching, the depth
	// limit and the Transfer outcome rules can't drift from the runtime. Only
	// the node effects are simulated (simDriver).
	d := &simDriver{in: &in, vars: res.Vars}
	for _, k := range in.DTMF {
		for _, r := range k {
			d.keys = append(d.keys, string(r)) // one keypress per digit, as a phone sends them
		}
	}
	path, err := ivr.Walk(context.Background(), flow, flow.EntryNode, d)
	if path != nil {
		res.Steps = path
	}
	if err != nil {
		res.Error = err.Error()
		if len(path) > 0 {
			res.EndedAt = path[len(path)-1].NodeID
			res.HangupCause = "TEMPORARY_FAILURE"
		}
		return res
	}
	if len(path) == 0 {
		return res
	}
	last := path[len(path)-1]
	res.EndedAt = last.NodeID
	switch {
	case last.Outcome == "" && last.NodeType == nodeHangup:
		res.HangupCause = "NORMAL_CLEARING"
	case last.Outcome == "":
		res.HangupCause = "END_OF_FLOW"
	default:
		// Walk stopped because no edge matches the outcome. A live call ends
		// here too, almost certainly not what the author meant: flag it.
		res.Error = fmt.Sprintf("node %s has no edge for outcome %q; a live call ends here", last.NodeID, last.Outcome)
		res.HangupCause = "TEMPORARY_FAILURE"
	}
	return res
}

// simDriver is the simulator's ivr.CallDriver: node effects from the
// operator's pre-staged input instead of a call — queued keypresses, forced
// Transfer / HTTP outcomes and a fixed clock. Menu and Gather consume keys the
// way the runtime driver (ivrdrive) does, so a trace matches a live call.
type simDriver struct {
	in   *SimInput
	keys []string // remaining keypresses, one digit each
	vars map[string]string
}

var _ ivr.CallDriver = (*simDriver)(nil)

// key pops the next queued keypress; ok=false when none is left (on a call,
// the wait for a digit would time out).
func (d *simDriver) key() (string, bool) {
	if len(d.keys) == 0 {
		return "", false
	}
	k := d.keys[0]
	d.keys = d.keys[1:]
	return k, true
}

func (d *simDriver) Greeting(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	if f := stringFromConfig(n.Config, "audio_file"); f != "" {
		return ivr.NodeResult{Outcome: "default", Action: "play_audio", Detail: f}, nil
	}
	if t := stringFromConfig(n.Config, "greeting_text"); t != "" {
		return ivr.NodeResult{Outcome: "default", Action: "speak", Detail: t}, nil
	}
	return ivr.NodeResult{Outcome: "default", Action: "play_audio", Detail: "(no audio configured)"}, nil
}

// Menu takes one key per attempt and re-prompts on an invalid one, up to
// max_retries, as a live menu does.
func (d *simDriver) Menu(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	detail := stringFromConfig(n.Config, "greeting_text")
	if detail == "" {
		detail = stringFromConfig(n.Config, "audio_file")
	}
	opts := mapFromConfig(n.Config, "options")
	retries := intFromConfig(n.Config, "max_retries", 3)
	last := ""
	for attempt := 0; attempt <= retries; attempt++ {
		k, ok := d.key()
		if !ok {
			return ivr.NodeResult{Outcome: "timeout", Action: "prompt_dtmf", Detail: detail, DTMF: last}, nil
		}
		if _, valid := opts[k]; valid {
			return ivr.NodeResult{Outcome: "digit:" + k, Action: "prompt_dtmf", Detail: detail, DTMF: k}, nil
		}
		last = k // invalid → re-prompt
	}
	return ivr.NodeResult{Outcome: "max_retries", Action: "prompt_dtmf", Detail: detail, DTMF: last}, nil
}

// Gather collects keys until the terminator (# by default), max_digits (or
// the older num_digits) or the end of the queue, as a live gather does.
func (d *simDriver) Gather(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	maxDigits := intFromConfig(n.Config, "max_digits", intFromConfig(n.Config, "num_digits", 10))
	minDigits := intFromConfig(n.Config, "min_digits", 1)
	terminator := stringFromConfig(n.Config, "terminator")
	if terminator == "" {
		terminator = "#"
	}
	detail := stringFromConfig(n.Config, "greeting_text")
	var got strings.Builder
	for got.Len() < maxDigits {
		k, ok := d.key()
		if !ok || k == terminator {
			break
		}
		got.WriteString(k)
	}
	if got.Len() == 0 || got.Len() < minDigits {
		return ivr.NodeResult{Outcome: "timeout", Action: "prompt_dtmf", Detail: detail, DTMF: got.String()}, nil
	}
	if store := stringFromConfig(n.Config, "store_as"); store != "" {
		d.vars[store] = got.String()
	}
	return ivr.NodeResult{Outcome: "default", Action: "prompt_dtmf", Detail: detail, DTMF: got.String()}, nil
}

// Transfer can't ring anyone: it takes the operator's forced outcome
// (completed by default). Walk applies the queue_timeout → no_answer rule.
func (d *simDriver) Transfer(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	return ivr.NodeResult{Outcome: transferOutcome(*d.in), Action: "transfer", Detail: transferDetail(n.Config)}, nil
}

// HTTPCallback makes no request — it follows the operator's chosen branch so
// both the success and failure routes can be tested.
func (d *simDriver) HTTPCallback(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	method := stringFromConfig(n.Config, "method")
	if method == "" {
		method = "GET"
	}
	return ivr.NodeResult{Outcome: httpOutcome(*d.in), Action: "http_call", Detail: method + " " + stringFromConfig(n.Config, "url")}, nil
}

// Timing evaluates the schedule against the simulator clock.
func (d *simDriver) Timing(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	outcome := evalTimingOutcome(n.Config, d.in.Now)
	open := "open"
	if outcome == "out_of_hours" {
		open = "closed"
	}
	return ivr.NodeResult{Outcome: outcome, Action: "branch", Detail: fmt.Sprintf("%s → %s", d.in.Now.Format("Mon 15:04"), open)}, nil
}

// GotoFlow doesn't recurse into the linked flow: the trace ends here with a
// note, so the operator tests the target flow separately.
func (d *simDriver) GotoFlow(_ context.Context, n *ivrNode) (ivr.NodeResult, error) {
	return ivr.NodeResult{Action: "branch", Detail: "would jump to flow " + stringFromConfig(n.Config, "flow_id")}, nil
}

func (d *simDriver) Hangup(context.Context, *ivrNode) (ivr.NodeResult, error) {
	return ivr.NodeResult{Action: "hangup"}, nil
}

// transferDetail renders the human label for a transfer node in a simulator
// trace: the pinned agent (agent_sip), the targeted team (with a "(priority)"
// suffix when the Priority checkbox is on), or "all agents". Kept in sync with
// runTransfer's stepDetail in ivr_executor.go.
func transferDetail(c map[string]interface{}) string {
	if number := stringFromConfig(c, "number"); number != "" {
		return "dial " + number // shown unresolved: the simulator makes no HTTP calls
	}
	if sip := stringFromConfig(c, "agent_sip"); sip != "" {
		return sip
	}
	if team := stringFromConfig(c, "team"); team != "" {
		if boolFromConfig(c, "priority") {
			return "team " + team + " (priority)"
		}
		return "team " + team
	}
	return "all agents"
}

// nextNode resolves the next node id by matching an outgoing edge whose
// condition equals the outcome — the same rule as a live call (ivr.Walk): no
// fallback to a default edge. ok=false when nothing matches; the call ends.
func nextNode(flow *ivrFlowData, from, outcome string) (string, bool) {
	next := ivr.FindNextNode(flow, from, outcome)
	return next, next != ""
}

// mapFromConfig / intFromConfig are shared with ivr_executor.go in
// this package, so no duplicate helper is needed here.
