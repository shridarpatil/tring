package brain

import (
	"testing"
	"time"
)

// Simulator tests are pure — no DB, no FS, no eslgo. They cover the
// branches a flow author hits while iterating: start→hangup, menus
// honoring queued DTMF, menu falling through to timeout when the
// queue runs dry, transfer recording the agent, and unreachable
// outcomes surfacing as Error.

const startHangupFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"end","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"end","condition":"default"}
  ]
}`

func TestSimulateStartToHangup(t *testing.T) {
	res, err := SimulateFlow([]byte(startHangupFlow), SimInput{})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(res.Steps), res.Steps)
	}
	if res.Steps[0].NodeType != "start" || res.Steps[1].NodeType != "hangup" {
		t.Errorf("node types = %s, %s, want start, hangup", res.Steps[0].NodeType, res.Steps[1].NodeType)
	}
	if res.EndedAt != "end" {
		t.Errorf("ended_at = %s, want 'end'", res.EndedAt)
	}
	if res.HangupCause != "NORMAL_CLEARING" {
		t.Errorf("hangup_cause = %s, want NORMAL_CLEARING", res.HangupCause)
	}
}

const greetingPlaybackFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"g","type":"greeting","config":{"audio_file":"ivr/welcome.wav"}},
    {"id":"end","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"g","condition":"default"},
    {"from":"g","to":"end","condition":"default"}
  ]
}`

func TestSimulateGreetingRecordsWouldPlay(t *testing.T) {
	res, err := SimulateFlow([]byte(greetingPlaybackFlow), SimInput{})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	g := res.Steps[1]
	if g.Action != "play_audio" {
		t.Errorf("action = %s, want play_audio", g.Action)
	}
	if g.Detail != "ivr/welcome.wav" {
		t.Errorf("detail = %s, want ivr/welcome.wav", g.Detail)
	}
}

const menuFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"m","type":"menu","config":{"options":{"1":{"label":"Sales"},"2":{"label":"Support"}}}},
    {"id":"sales","type":"hangup","config":{}},
    {"id":"support","type":"hangup","config":{}},
    {"id":"timeout","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"m","condition":"default"},
    {"from":"m","to":"sales","condition":"digit:1"},
    {"from":"m","to":"support","condition":"digit:2"},
    {"from":"m","to":"timeout","condition":"timeout"}
  ]
}`

func TestSimulateMenuPicksQueuedDTMF(t *testing.T) {
	res, err := SimulateFlow([]byte(menuFlow), SimInput{DTMF: []string{"2"}})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if res.EndedAt != "support" {
		t.Errorf("ended_at = %s, want 'support' (chose digit 2)", res.EndedAt)
	}
	mStep := res.Steps[1]
	if mStep.DTMFInput != "2" || mStep.Outcome != "digit:2" {
		t.Errorf("menu step = %+v, want dtmf=2 outcome=digit:2", mStep)
	}
}

func TestSimulateMenuFallsThroughToTimeoutWhenQueueEmpty(t *testing.T) {
	res, err := SimulateFlow([]byte(menuFlow), SimInput{DTMF: nil})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if res.EndedAt != "timeout" {
		t.Errorf("ended_at = %s, want 'timeout' (no DTMF supplied)", res.EndedAt)
	}
	mStep := res.Steps[1]
	if mStep.Outcome != "timeout" {
		t.Errorf("menu outcome = %s, want timeout", mStep.Outcome)
	}
}

const transferFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"t","type":"transfer","config":{"agent_sip":"1001"}},
    {"id":"end","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"t","condition":"default"},
    {"from":"t","to":"end","condition":"completed"}
  ]
}`

func TestSimulateTransferRecordsAgent(t *testing.T) {
	res, err := SimulateFlow([]byte(transferFlow), SimInput{})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	tStep := res.Steps[1]
	if tStep.Action != "transfer" || tStep.Detail != "1001" {
		t.Errorf("transfer step = %+v, want action=transfer detail=1001", tStep)
	}
}

const danglingOutcomeFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}}
  ],
  "edges": []
}`

func TestSimulateSurfacesUnreachableOutcome(t *testing.T) {
	res, err := SimulateFlow([]byte(danglingOutcomeFlow), SimInput{})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if res.Error == "" {
		t.Errorf("expected Error populated for missing edge; got %+v", res)
	}
}

const timingFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"t","type":"timing","config":{"schedule":[{"day":"friday","enabled":true,"start_time":"09:00","end_time":"18:00"}]}},
    {"id":"open","type":"hangup","config":{}},
    {"id":"closed","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"t","condition":"default"},
    {"from":"t","to":"open","condition":"in_hours"},
    {"from":"t","to":"closed","condition":"out_of_hours"}
  ]
}`

func TestSimulateTimingUsesSuppliedClock(t *testing.T) {
	morning, _ := time.Parse(time.RFC3339, "2026-05-29T10:00:00Z")
	night, _ := time.Parse(time.RFC3339, "2026-05-29T23:00:00Z")

	open, _ := SimulateFlow([]byte(timingFlow), SimInput{Now: morning})
	if open.EndedAt != "open" {
		t.Errorf("morning ended_at = %s, want 'open'", open.EndedAt)
	}
	closed, _ := SimulateFlow([]byte(timingFlow), SimInput{Now: night})
	if closed.EndedAt != "closed" {
		t.Errorf("night ended_at = %s, want 'closed'", closed.EndedAt)
	}
}
