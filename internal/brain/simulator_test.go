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

func TestTransferDetailNumber(t *testing.T) {
	got := transferDetail(map[string]interface{}{"number": "{{dest}}", "team": "sales"})
	if got != "dial {{dest}}" {
		t.Fatalf("detail = %q, want the number (it takes precedence over team)", got)
	}
}

const pinGatherFlow = `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"pin","type":"gather","config":{"min_digits":4,"max_digits":4,"store_as":"pin"}},
    {"id":"ok","type":"hangup","config":{}},
    {"id":"retry","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"pin","condition":"default"},
    {"from":"pin","to":"ok","condition":"default"},
    {"from":"pin","to":"retry","condition":"timeout"}
  ]
}`

func TestSimulateGatherBelowMinDigitsTimesOut(t *testing.T) {
	res, err := SimulateFlow([]byte(pinGatherFlow), SimInput{DTMF: []string{"1", "2"}})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if res.EndedAt != "retry" {
		t.Errorf("ended_at = %s, want 'retry' (2 digits < min 4)", res.EndedAt)
	}
	if _, ok := res.Vars["pin"]; ok {
		t.Errorf("pin stored as %q, want unset", res.Vars["pin"])
	}

	res, err = SimulateFlow([]byte(pinGatherFlow), SimInput{DTMF: []string{"1", "2", "3", "4"}})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if res.EndedAt != "ok" || res.Vars["pin"] != "1234" {
		t.Errorf("ended_at = %s pin = %q, want ok / 1234", res.EndedAt, res.Vars["pin"])
	}
}

// The simulator takes a forced queue_timeout down the no_answer edge when the
// flow has no queue_timeout edge, exactly as a live call does (ivr.Walk).
func TestSimulateQueueTimeoutFallsBackToNoAnswer(t *testing.T) {
	flow := `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"t","type":"transfer","config":{"team":"sales"}},
    {"id":"na","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"t","condition":"default"},
    {"from":"t","to":"na","condition":"no_answer"}
  ]
}`
	res, err := SimulateFlow([]byte(flow), SimInput{TransferOutcome: "queue_timeout"})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	if res.Error != "" || res.EndedAt != "na" || res.Steps[1].Outcome != "no_answer" {
		t.Errorf("error=%q ended_at=%s transfer outcome=%q, want no error / na / no_answer", res.Error, res.EndedAt, res.Steps[1].Outcome)
	}
}

func TestInteractiveGatherBelowMinDigitsTimesOut(t *testing.T) {
	node := &ivrNode{ID: "pin", Type: nodeGather, Config: map[string]interface{}{
		"min_digits": float64(4), "store_as": "pin",
	}}
	vars := map[string]string{}
	if _, oc := stepForInteractive(node, "12", vars, time.Now()); oc != "timeout" {
		t.Errorf("outcome = %s, want timeout (2 digits < min 4)", oc)
	}
	if _, ok := vars["pin"]; ok {
		t.Errorf("pin stored as %q, want unset", vars["pin"])
	}
}

// The Test panel must walk a flow exactly as a live call does (ivr.Walk).
// These pin the places the simulator's own loop used to disagree.

// A live call whose outcome has no edge ends there; it does not fall back to
// the node's default edge.
func TestSimulateNoDefaultEdgeFallback(t *testing.T) {
	flow := `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"m","type":"menu","config":{"options":{"1":{"label":"A"}}}},
    {"id":"other","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"m","condition":"default"},
    {"from":"m","to":"other","condition":"default"}
  ]
}`
	res, err := SimulateFlow([]byte(flow), SimInput{}) // no DTMF → the menu times out
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	for _, s := range res.Steps {
		if s.NodeID == "other" {
			t.Fatalf("a timeout with no timeout edge followed the default edge; steps %+v", res.Steps)
		}
	}
	if res.EndedAt != "m" || res.Error == "" {
		t.Fatalf("ended at %q (error %q), want the call ending at the menu with the missing edge flagged", res.EndedAt, res.Error)
	}
}

// A live menu re-prompts on an invalid digit (up to max_retries), so a valid
// digit pressed after an invalid one is taken.
func TestSimulateMenuRetriesAfterInvalidDigit(t *testing.T) {
	res, err := SimulateFlow([]byte(menuFlow), SimInput{DTMF: []string{"9", "1"}})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	var menu SimStep
	for _, s := range res.Steps {
		if s.NodeType == nodeMenu {
			menu = s
		}
	}
	if menu.Outcome != "digit:1" {
		t.Fatalf("menu outcome = %q, want digit:1 after re-prompting past the invalid 9", menu.Outcome)
	}
}

// Invalid digits on every attempt (1 + max_retries) take max_retries.
func TestSimulateMenuMaxRetries(t *testing.T) {
	res, err := SimulateFlow([]byte(menuFlow), SimInput{DTMF: []string{"8", "8", "8", "8"}})
	if err != nil {
		t.Fatalf("SimulateFlow: %v", err)
	}
	for _, s := range res.Steps {
		if s.NodeType == nodeMenu && s.Outcome != "max_retries" {
			t.Fatalf("menu outcome = %q, want max_retries", s.Outcome)
		}
	}
}

// A live gather stops at the terminator (# by default) and reads num_digits
// when max_digits isn't set.
func TestSimulateGatherHonoursTerminatorAndNumDigits(t *testing.T) {
	flow := func(cfg string) string {
		return `{
  "version": 2,
  "entry_node": "start",
  "nodes": [
    {"id":"start","type":"start","config":{}},
    {"id":"g","type":"gather","config":` + cfg + `},
    {"id":"end","type":"hangup","config":{}}
  ],
  "edges": [
    {"from":"start","to":"g","condition":"default"},
    {"from":"g","to":"end","condition":"default"},
    {"from":"g","to":"end","condition":"timeout"}
  ]
}`
	}
	gathered := func(cfg string, dtmf ...string) string {
		t.Helper()
		res, err := SimulateFlow([]byte(flow(cfg)), SimInput{DTMF: dtmf})
		if err != nil {
			t.Fatalf("SimulateFlow: %v", err)
		}
		return res.Vars["acct"]
	}
	if got := gathered(`{"store_as":"acct"}`, "1", "2", "#", "3"); got != "12" {
		t.Errorf("terminator: gathered %q, want 12", got)
	}
	if got := gathered(`{"store_as":"acct","num_digits":3}`, "1", "2", "3", "4"); got != "123" {
		t.Errorf("num_digits: gathered %q, want 123", got)
	}
}
