package ivrdrive_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
)

// fakePrompts records played files and notifies a channel so the test can feed
// DTMF exactly when a prompt plays (the driver drains DTMF before each prompt,
// so feeding after the play notification is race-free). Play returns at once, so
// it models a prompt that has already finished by the time DTMF is fed — fine
// for the routing/timeout tests, but barge-in needs a prompt that's still
// playing (see blockingPrompts).
type fakePrompts struct{ played chan string }

func (f *fakePrompts) Play(_ context.Context, file string) error {
	f.played <- file
	return nil
}

func (f *fakePrompts) Stop() {}

// blockingPrompts models a prompt that is STILL PLAYING: Play blocks until Stop
// flushes it (barge-in) or ctx is cancelled. started is signalled with the file
// when each prompt begins; stops counts Stop calls. This is what lets a test
// prove a digit interrupts an in-progress prompt rather than waiting it out.
type blockingPrompts struct {
	started chan string
	mu      sync.Mutex
	cur     chan struct{} // closed by Stop to end the current Play
	stops   int
}

func newBlockingPrompts() *blockingPrompts {
	return &blockingPrompts{started: make(chan string, 8)}
}

func (b *blockingPrompts) Play(ctx context.Context, file string) error {
	b.mu.Lock()
	ch := make(chan struct{})
	b.cur = ch
	b.mu.Unlock()
	b.started <- file
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingPrompts) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	if b.cur != nil {
		select {
		case <-b.cur:
		default:
			close(b.cur)
		}
		b.cur = nil
	}
}

func (b *blockingPrompts) stopCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stops
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

// bargeMenuFlow: start → menu(menu.ogg, option 1) --digit:1--> h(hangup, no
// audio) / --timeout--> h2(hangup, no audio). The hangup nodes have no prompt so
// a blocking prompt fake can't stall there — only the menu prompt blocks.
func bargeMenuFlow() *ivr.Flow {
	return &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "m", Type: ivr.NodeMenu, Config: map[string]interface{}{
				"audio_file": "menu.ogg", "timeout_seconds": float64(1),
				"options": map[string]interface{}{"1": "sales"},
			}},
			{ID: "h", Type: ivr.NodeHangup},
			{ID: "h2", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{
			{From: "s", To: "m", Condition: "default"},
			{From: "m", To: "h", Condition: "digit:1"},
			{From: "m", To: "h2", Condition: "timeout"},
		},
	}
}

// TestMenuValidDigitBargesInDuringPrompt: a valid option pressed WHILE the menu
// prompt is still playing must stop the prompt and advance immediately, not wait
// for the prompt to finish. With a still-playing prompt fake, the flow only
// completes if the driver interrupts playback on the digit.
func TestMenuValidDigitBargesInDuringPrompt(t *testing.T) {
	b := newBlockingPrompts()
	d := ivrdrive.New(ivrdrive.Config{Prompts: b})
	pathCh := make(chan []ivr.Step, 1)
	go func() {
		p, _ := ivr.Walk(context.Background(), bargeMenuFlow(), "s", d)
		pathCh <- p
	}()

	select {
	case f := <-b.started:
		if f != "menu.ogg" {
			t.Fatalf("started %q, want menu.ogg", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("menu prompt never started playing")
	}

	d.DTMF("1") // valid option pressed mid-prompt

	select {
	case p := <-pathCh:
		if want := []string{"s", "m", "h"}; !eq(ids(p), want) {
			t.Fatalf("path = %v, want %v", ids(p), want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("barge-in did not interrupt the prompt: flow hung until prompt end")
	}
	if b.stopCount() < 1 {
		t.Fatal("Stop() never called — prompt was not interrupted on the digit")
	}
}

// TestMenuInvalidDigitDoesNotInterruptPrompt locks in the chosen semantics: an
// invalid digit pressed during the prompt is ignored (prompt keeps playing); only
// a valid option barges in. If an invalid digit wrongly interrupted, the menu
// would treat it as a bad choice and replay the prompt (menu.ogg twice) or fall
// to timeout — so a single play + the digit:1 path proves invalid was ignored.
func TestMenuInvalidDigitDoesNotInterruptPrompt(t *testing.T) {
	b := newBlockingPrompts()
	d := ivrdrive.New(ivrdrive.Config{Prompts: b})
	pathCh := make(chan []ivr.Step, 1)
	go func() {
		p, _ := ivr.Walk(context.Background(), bargeMenuFlow(), "s", d)
		pathCh <- p
	}()

	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		t.Fatal("menu prompt never started playing")
	}

	d.DTMF("9") // invalid — must NOT interrupt the prompt
	d.DTMF("1") // valid — should barge in

	select {
	case p := <-pathCh:
		if want := []string{"s", "m", "h"}; !eq(ids(p), want) {
			t.Fatalf("path = %v, want %v (invalid digit must not cause retry/timeout)", ids(p), want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flow hung")
	}
	if got := d.Played(); len(got) != 1 || got[0] != "menu.ogg" {
		t.Fatalf("played = %v, want exactly one menu.ogg (invalid digit triggered a replay)", got)
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
