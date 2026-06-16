package ivrdrive_test

import (
	"context"
	"testing"
	"time"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/ivrdrive"
)

// fakePrompts records played files and notifies a channel so the test can feed
// DTMF exactly when a prompt plays (the driver drains DTMF before each prompt,
// so feeding after the play notification is race-free).
type fakePrompts struct{ played chan string }

func (f *fakePrompts) Play(_ context.Context, file string) error {
	f.played <- file
	return nil
}

var _ ivr.CallDriver = (*ivrdrive.Driver)(nil)

// runFlow walks flow with d, reactively sending DTMF: when a prompt file plays,
// it sends react[file] digits. Returns the traversed path.
func runFlow(t *testing.T, d *ivrdrive.Driver, fake *fakePrompts, flow *ivr.Flow, react map[string][]string) []ivr.Step {
	t.Helper()
	done := make(chan struct{})
	var path []ivr.Step
	go func() {
		path, _ = ivr.Walk(context.Background(), flow, flow.EntryNode, d)
		close(done)
	}()
	for {
		select {
		case file := <-fake.played:
			for _, dg := range react[file] {
				d.DTMF(dg)
			}
		case <-done:
			return path
		case <-time.After(5 * time.Second):
			t.Fatal("flow walk hung")
		}
	}
}

func ids(path []ivr.Step) []string {
	out := make([]string, len(path))
	for i, s := range path {
		out[i] = s.NodeID
	}
	return out
}
func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newDriver(fake *fakePrompts, now func() time.Time) *ivrdrive.Driver {
	return ivrdrive.New(ivrdrive.Config{Prompts: fake, Now: now})
}

// fakeAgents records the ring target and returns a scripted outcome.
type fakeAgents struct {
	outcome string
	gotRoom string
	gotTgt  ivrdrive.Target
}

func (a *fakeAgents) Ring(_ context.Context, room string, t ivrdrive.Target) (string, error) {
	a.gotRoom = room
	a.gotTgt = t
	return a.outcome, nil
}

// menuFlow: start → greeting(greeting.ogg) → menu(menu.ogg) --digit:1--> hangup(bye.ogg)
//
//	--timeout--> hangup2(timeout.ogg)
func menuFlow() *ivr.Flow {
	return &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "g", Type: ivr.NodeGreeting, Config: map[string]interface{}{"audio_file": "greeting.ogg"}},
			{ID: "m", Type: ivr.NodeMenu, Config: map[string]interface{}{
				"audio_file": "menu.ogg", "timeout_seconds": float64(1),
				"options": map[string]interface{}{"1": "sales", "2": "support"},
			}},
			{ID: "h", Type: ivr.NodeHangup, Config: map[string]interface{}{"audio_file": "bye.ogg"}},
			{ID: "h2", Type: ivr.NodeHangup, Config: map[string]interface{}{"audio_file": "timeout.ogg"}},
		},
		Edges: []ivr.Edge{
			{From: "s", To: "g", Condition: "default"},
			{From: "g", To: "m", Condition: "default"},
			{From: "m", To: "h", Condition: "digit:1"},
			{From: "m", To: "h2", Condition: "timeout"},
		},
	}
}

func TestMenuDigitRoutesAndPlaysPrompts(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	d := newDriver(fake, nil)
	path := runFlow(t, d, fake, menuFlow(), map[string][]string{"menu.ogg": {"1"}})

	if want := []string{"s", "g", "m", "h"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
	if want := []string{"greeting.ogg", "menu.ogg", "bye.ogg"}; !eq(d.Played(), want) {
		t.Fatalf("played = %v, want %v", d.Played(), want)
	}
}

func TestMenuTimeoutBranch(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	d := newDriver(fake, nil)
	// no DTMF fed → menu times out (1s) → timeout branch
	path := runFlow(t, d, fake, menuFlow(), nil)
	if want := []string{"s", "g", "m", "h2"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
}

func TestGatherCollectsAndStores(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	d := newDriver(fake, nil)
	flow := &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "ga", Type: ivr.NodeGather, Config: map[string]interface{}{
				"audio_file": "pin.ogg", "num_digits": float64(3),
				"timeout_seconds": float64(1), "store_as": "pin",
			}},
			{ID: "h", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{
			{From: "s", To: "ga", Condition: "default"},
			{From: "ga", To: "h", Condition: "default"},
		},
	}
	path := runFlow(t, d, fake, flow, map[string][]string{"pin.ogg": {"1", "2", "3"}})
	if want := []string{"s", "ga", "h"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
	if got := d.Vars()["pin"]; got != "123" {
		t.Fatalf("gathered pin = %q, want 123", got)
	}
}

func TestTransferRoutesToAgent(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	agents := &fakeAgents{outcome: "completed"}
	d := ivrdrive.New(ivrdrive.Config{Prompts: fake, Agents: agents, Room: "tenant/call", ClientRef: "Ticket #42"})
	flow := &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "x", Type: ivr.NodeTransfer, Config: map[string]interface{}{"team": "sales"}},
			{ID: "done", Type: ivr.NodeHangup},
			{ID: "vm", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{
			{From: "s", To: "x", Condition: "default"},
			{From: "x", To: "done", Condition: "completed"},
			{From: "x", To: "vm", Condition: "no_answer"},
		},
	}
	path := runFlow(t, d, fake, flow, nil)
	if want := []string{"s", "x", "done"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v (transfer → completed)", ids(path), want)
	}
	if agents.gotRoom != "tenant/call" {
		t.Fatalf("ring room = %q, want tenant/call", agents.gotRoom)
	}
	if agents.gotTgt.Team != "sales" || agents.gotTgt.Label != "Ticket #42" {
		t.Fatalf("ring target = %+v, want team=sales label=Ticket #42", agents.gotTgt)
	}
}

func TestTransferNoAnswerBranches(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	agents := &fakeAgents{outcome: "no_answer"}
	d := ivrdrive.New(ivrdrive.Config{Prompts: fake, Agents: agents, Room: "r"})
	flow := &ivr.Flow{
		EntryNode: "x",
		Nodes: []ivr.Node{
			{ID: "x", Type: ivr.NodeTransfer, Config: map[string]interface{}{"agent_sip": "alice"}},
			{ID: "done", Type: ivr.NodeHangup},
			{ID: "vm", Type: ivr.NodeHangup, Config: map[string]interface{}{"audio_file": "voicemail.ogg"}},
		},
		Edges: []ivr.Edge{
			{From: "x", To: "done", Condition: "completed"},
			{From: "x", To: "vm", Condition: "no_answer"},
		},
	}
	path := runFlow(t, d, fake, flow, nil)
	if want := []string{"x", "vm"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v (no_answer → voicemail)", ids(path), want)
	}
}

func TestTransferWithoutAgentsReportsNoAnswer(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	d := ivrdrive.New(ivrdrive.Config{Prompts: fake}) // no Agents configured
	flow := &ivr.Flow{
		EntryNode: "x",
		Nodes: []ivr.Node{
			{ID: "x", Type: ivr.NodeTransfer, Config: map[string]interface{}{"team": "sales"}},
			{ID: "vm", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{{From: "x", To: "vm", Condition: "no_answer"}},
	}
	path := runFlow(t, d, fake, flow, nil)
	if want := []string{"x", "vm"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
}

func TestTimingUsesInjectedClock(t *testing.T) {
	fake := &fakePrompts{played: make(chan string, 16)}
	// Sunday 03:00 — a schedule open Mon–Fri 09:00–17:00 is closed → out_of_hours.
	sunday3am := func() time.Time { return time.Date(2026, 6, 14, 3, 0, 0, 0, time.UTC) }
	d := newDriver(fake, sunday3am)
	flow := &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "tm", Type: ivr.NodeTiming, Config: map[string]interface{}{
				"schedule": []interface{}{
					map[string]interface{}{"day": "monday", "enabled": true, "start_time": "09:00", "end_time": "17:00"},
				},
			}},
			{ID: "open", Type: ivr.NodeHangup},
			{ID: "closed", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{
			{From: "s", To: "tm", Condition: "default"},
			{From: "tm", To: "open", Condition: "in_hours"},
			{From: "tm", To: "closed", Condition: "out_of_hours"},
		},
	}
	path := runFlow(t, d, fake, flow, nil)
	if want := []string{"s", "tm", "closed"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v (timing should be out_of_hours on Sunday)", ids(path), want)
	}
}
