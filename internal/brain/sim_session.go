// Interactive simulator session — the operator drives the call live
// from the editor. Walker pauses on every Menu / Gather node, surfaces
// the prompt, and waits for the next API call to supply input. State
// lives in process memory (sync.Map keyed by session UUID); fine for
// a developer tool, no need to survive restarts.

package brain

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// sessionTTL is how long an idle session lingers before GC. Long enough
// to take a phone call mid-edit, short enough that an abandoned editor
// tab doesn't leak.
const sessionTTL = 10 * time.Minute

// SessionStatus is what the client looks at to decide whether to render
// the input box or the "ended" summary.
type SessionStatus string

const (
	StatusAwaitingDTMF SessionStatus = "awaiting_dtmf"
	StatusEnded        SessionStatus = "ended"
	StatusError        SessionStatus = "error"
)

// AwaitingNode is the minimal payload the UI needs to render a prompt
// — node id, type, and whatever audio/text the call would play here.
type AwaitingNode struct {
	NodeID   string `json:"node_id"`
	NodeType string `json:"node_type"`
	Prompt   string `json:"prompt,omitempty"`
}

// SimSnapshot is what the API hands back after every start/input call.
// Steps is the cumulative trace since session start, so the client
// can replay the whole transcript without keeping its own copy.
type SimSnapshot struct {
	SessionID    string            `json:"session_id"`
	Status       SessionStatus     `json:"status"`
	Steps        []SimStep         `json:"steps"`
	Vars         map[string]string `json:"vars"`
	AwaitingNode *AwaitingNode     `json:"awaiting_node,omitempty"`
	EndedAt      string            `json:"ended_at,omitempty"`
	HangupCause  string            `json:"hangup_cause,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// session is the in-memory record. Not exported — clients see only
// the SimSnapshot view.
type simSession struct {
	id          uuid.UUID
	owner       uuid.UUID // tenant that started it; only it may drive the session
	flow        *ivrFlowData
	currentNode string // where to resume; "" once ended
	vars        map[string]string
	steps       []SimStep
	depth       int
	caller      string
	awaiting    *AwaitingNode
	ended       bool
	endedAt     string
	hangupCause string
	errString   string
	now         time.Time // clock for Timing nodes (from SimInput.Now), fixed at Start
	lastTouched time.Time
}

// SimManager owns the live sessions. One instance per api process —
// constructed in server.go and held on *Server.
type SimManager struct {
	sessions sync.Map // map[uuid.UUID]*simSession
}

func NewSimManager() *SimManager { return &SimManager{} }

// Start creates a new session owned by tenant owner, walks until the first
// pause / end, and returns the snapshot.
func (m *SimManager) Start(owner uuid.UUID, flowJSON []byte, in SimInput) (*SimSnapshot, error) {
	flow, err := parseFlow(flowJSON)
	if err != nil {
		return nil, err
	}
	if in.Caller == "" {
		in.Caller = "+10000000000"
	}
	if in.Now.IsZero() {
		in.Now = time.Now() // no fixture clock → wall clock (same default as the batch simulator)
	}
	s := &simSession{
		id:          uuid.New(),
		owner:       owner,
		flow:        flow,
		currentNode: flow.EntryNode,
		vars:        copyVars(in.Vars),
		caller:      in.Caller,
		now:         in.Now,
		lastTouched: time.Now(),
	}
	m.sessions.Store(s.id, s)
	m.runUntilPause(s, "")
	m.gcExpired()
	return snapshot(s), nil
}

// Input feeds the supplied DTMF into the paused node and continues
// walking. Returns "session not found" if the session has been GC'd or
// belongs to another tenant (indistinguishable, so a foreign session id
// reveals nothing); the UI should restart in that case.
func (m *SimManager) Input(owner uuid.UUID, sessionID string, dtmf string) (*SimSnapshot, error) {
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session id")
	}
	v, ok := m.sessions.Load(id)
	if !ok || v.(*simSession).owner != owner {
		return nil, fmt.Errorf("session not found")
	}
	s := v.(*simSession)
	s.lastTouched = time.Now()
	if s.ended {
		return snapshot(s), nil
	}
	m.runUntilPause(s, dtmf)
	return snapshot(s), nil
}

// runUntilPause walks the flow from s.currentNode, applying `input`
// to the first paused-on node (if any), then keeps going until the
// next pause point or the flow ends.
func (m *SimManager) runUntilPause(s *simSession, input string) {
	// Reset pause state before each run.
	s.awaiting = nil

	for ; s.depth < 64 && s.currentNode != ""; s.depth++ {
		node, ok := findNode(s.flow, s.currentNode)
		if !ok {
			s.errString = fmt.Sprintf("node %q not found", s.currentNode)
			s.ended = true
			s.hangupCause = "TEMPORARY_FAILURE"
			return
		}

		// Menu and Gather pause for input. If we have input queued
		// (either from the current call or from a prior queue), consume
		// it; otherwise pause and surface the prompt.
		if node.Type == nodeMenu || node.Type == nodeGather {
			if input == "" {
				s.awaiting = &AwaitingNode{
					NodeID:   node.ID,
					NodeType: node.Type,
					Prompt:   promptFor(node),
				}
				return
			}
		}

		step, outcome := stepForInteractive(node, input, s.vars, s.now)
		input = "" // input is consumed by exactly one node
		s.steps = append(s.steps, step)

		if outcome == "" {
			// Terminal.
			s.endedAt = node.ID
			if node.Type == nodeHangup {
				s.hangupCause = "NORMAL_CLEARING"
			} else {
				s.hangupCause = "END_OF_FLOW"
			}
			s.currentNode = ""
			s.ended = true
			return
		}

		next, ok := nextNode(s.flow, node.ID, outcome)
		if !ok {
			s.errString = fmt.Sprintf("node %s has no edge for outcome %q", node.ID, outcome)
			s.endedAt = node.ID
			s.hangupCause = "TEMPORARY_FAILURE"
			s.currentNode = ""
			s.ended = true
			return
		}
		s.currentNode = next
	}

	if s.depth >= 64 {
		s.errString = "exceeded max walk depth (64)"
		s.ended = true
	}
}

// stepForInteractive is the interactive counterpart of stepFor.
// `input` is the live DTMF for menu/gather; ignored for other nodes.
func stepForInteractive(node *ivrNode, input string, vars map[string]string, now time.Time) (SimStep, string) {
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
		step.Detail = promptFor(node)
		opts := mapFromConfig(node.Config, "options")
		step.DTMFInput = input
		if _, ok := opts[input]; ok {
			step.Outcome = "digit:" + input
			return step, "digit:" + input
		}
		step.Outcome = "max_retries"
		return step, "max_retries"

	case nodeGather:
		step.Action = "prompt_dtmf"
		step.Detail = promptFor(node)
		step.DTMFInput = input
		if storeAs, ok := node.Config["store_as"].(string); ok && storeAs != "" {
			vars[storeAs] = input
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
		step.Outcome = "http:2xx"
		return step, "http:2xx"

	case nodeTransfer:
		step.Action = "transfer"
		step.Detail = transferDetail(node.Config)
		step.Outcome = "completed"
		return step, "completed"

	case nodeGotoFlow:
		step.Action = "branch"
		flowID, _ := node.Config["flow_id"].(string)
		step.Detail = "would jump to flow " + flowID
		step.Outcome = ""
		return step, ""

	case nodeTiming:
		step.Action = "branch"
		step.Outcome = evalTimingOutcome(node.Config, now)
		if step.Outcome == "out_of_hours" {
			step.Detail = "closed"
		} else {
			step.Detail = "open"
		}
		return step, step.Outcome

	case nodeHangup:
		step.Action = "hangup"
		step.Outcome = ""
		return step, ""

	default:
		step.Action = "unknown"
		step.Outcome = ""
		return step, ""
	}
}

// promptFor returns the human-readable prompt the UI shows at a
// pause point — greeting_text if set, else audio_file, else a generic
// note tied to the node label.
func promptFor(node *ivrNode) string {
	if t, ok := node.Config["greeting_text"].(string); ok && t != "" {
		return t
	}
	if f, ok := node.Config["audio_file"].(string); ok && f != "" {
		return "play: " + f
	}
	return node.Label
}

// gcExpired drops sessions idle past TTL. Lazy / called from Start so
// we don't need a background goroutine.
func (m *SimManager) gcExpired() {
	cutoff := time.Now().Add(-sessionTTL)
	m.sessions.Range(func(k, v interface{}) bool {
		s := v.(*simSession)
		if s.lastTouched.Before(cutoff) {
			m.sessions.Delete(k)
		}
		return true
	})
}

// snapshot is the in-memory→wire-shape projection. Keeps internal
// fields (depth, lastTouched, flow pointer) off the response.
func snapshot(s *simSession) *SimSnapshot {
	out := &SimSnapshot{
		SessionID: s.id.String(),
		Steps:     s.steps,
		Vars:      s.vars,
	}
	switch {
	case s.errString != "":
		out.Status = StatusError
		out.Error = s.errString
		out.EndedAt = s.endedAt
		out.HangupCause = s.hangupCause
	case s.ended:
		out.Status = StatusEnded
		out.EndedAt = s.endedAt
		out.HangupCause = s.hangupCause
	case s.awaiting != nil:
		out.Status = StatusAwaitingDTMF
		out.AwaitingNode = s.awaiting
	default:
		// Defensive: shouldn't happen — walker always pauses or ends.
		out.Status = StatusError
		out.Error = "walker returned without pause or end"
	}
	return out
}

func parseFlow(raw []byte) (*ivrFlowData, error) {
	var f ivrFlowData
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse flow: %w", err)
	}
	if f.EntryNode == "" {
		return nil, fmt.Errorf("flow has no entry node")
	}
	return &f, nil
}

func copyVars(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
