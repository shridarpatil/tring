package ivr_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/ivr"
)

// scriptDriver is a fake CallDriver: each node-effect returns a scripted outcome
// (keyed by node id), so Walk's traversal can be tested deterministically with
// no FreeSWITCH/pion.
type scriptDriver struct {
	outcomes map[string]string // node id → outcome
	failOn   string            // node id to return an error from
}

func (d *scriptDriver) out(n *ivr.Node) (ivr.NodeResult, error) {
	if n.ID == d.failOn {
		return ivr.NodeResult{}, errors.New("boom")
	}
	return ivr.NodeResult{Outcome: d.outcomes[n.ID]}, nil
}
func (d *scriptDriver) Greeting(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.out(n)
}
func (d *scriptDriver) Menu(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) { return d.out(n) }
func (d *scriptDriver) Gather(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.out(n)
}
func (d *scriptDriver) Transfer(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.out(n)
}
func (d *scriptDriver) HTTPCallback(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.out(n)
}
func (d *scriptDriver) Timing(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.out(n)
}
func (d *scriptDriver) GotoFlow(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.out(n)
}
func (d *scriptDriver) Hangup(_ context.Context, _ *ivr.Node) (ivr.NodeResult, error) {
	return ivr.NodeResult{}, nil
}

var _ ivr.CallDriver = (*scriptDriver)(nil)

// flow: start → greeting → menu --digit:1--> transfer → hangup
//
//	--timeout---> hangup
func sampleFlow() *ivr.Flow {
	return &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "g", Type: ivr.NodeGreeting},
			{ID: "m", Type: ivr.NodeMenu},
			{ID: "t", Type: ivr.NodeTransfer},
			{ID: "h", Type: ivr.NodeHangup},
		},
		Edges: []ivr.Edge{
			{From: "s", To: "g", Condition: "default"},
			{From: "g", To: "m", Condition: "default"},
			{From: "m", To: "t", Condition: "digit:1"},
			{From: "m", To: "h", Condition: "timeout"},
			{From: "t", To: "h", Condition: "completed"},
		},
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

func TestWalkFollowsMenuChoice(t *testing.T) {
	d := &scriptDriver{outcomes: map[string]string{"g": "default", "m": "digit:1", "t": "completed"}}
	path, err := ivr.Walk(context.Background(), sampleFlow(), "s", d)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if want := []string{"s", "g", "m", "t", "h"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
	if path[len(path)-1].Outcome != "" {
		t.Fatalf("last step (hangup) outcome = %q, want terminal \"\"", path[len(path)-1].Outcome)
	}
}

func TestWalkFollowsTimeoutBranch(t *testing.T) {
	d := &scriptDriver{outcomes: map[string]string{"g": "default", "m": "timeout"}}
	path, _ := ivr.Walk(context.Background(), sampleFlow(), "s", d)
	if want := []string{"s", "g", "m", "h"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
}

func TestWalkEndsWhenNoEdgeForOutcome(t *testing.T) {
	// menu returns an outcome with no matching edge → graceful end.
	d := &scriptDriver{outcomes: map[string]string{"g": "default", "m": "digit:9"}}
	path, err := ivr.Walk(context.Background(), sampleFlow(), "s", d)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if want := []string{"s", "g", "m"}; !eq(ids(path), want) {
		t.Fatalf("path = %v, want %v", ids(path), want)
	}
}

func TestWalkReturnsDriverError(t *testing.T) {
	d := &scriptDriver{outcomes: map[string]string{"g": "default"}, failOn: "m"}
	path, err := ivr.Walk(context.Background(), sampleFlow(), "s", d)
	if err == nil {
		t.Fatal("expected error from failing node")
	}
	if want := []string{"s", "g", "m"}; !eq(ids(path), want) {
		t.Fatalf("partial path = %v, want %v", ids(path), want)
	}
}

func TestWalkUnknownEntryErrors(t *testing.T) {
	d := &scriptDriver{outcomes: map[string]string{}}
	if _, err := ivr.Walk(context.Background(), sampleFlow(), "nope", d); err == nil {
		t.Fatal("expected error for unknown entry node")
	}
}

// richDriver returns a full NodeResult per node id, so we can assert Walk
// records Action/Detail/DTMF (not just the outcome). This is the journey-shape
// coverage that used to live in the orchestrator's TestRecordStepBuildsJourney.
type richDriver struct{ results map[string]ivr.NodeResult }

func (d *richDriver) r(n *ivr.Node) (ivr.NodeResult, error)                           { return d.results[n.ID], nil }
func (d *richDriver) Greeting(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) { return d.r(n) }
func (d *richDriver) Menu(_ context.Context, n *ivr.Node) (ivr.NodeResult, error)     { return d.r(n) }
func (d *richDriver) Gather(_ context.Context, n *ivr.Node) (ivr.NodeResult, error)   { return d.r(n) }
func (d *richDriver) Transfer(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) { return d.r(n) }
func (d *richDriver) HTTPCallback(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return d.r(n)
}
func (d *richDriver) Timing(_ context.Context, n *ivr.Node) (ivr.NodeResult, error)   { return d.r(n) }
func (d *richDriver) GotoFlow(_ context.Context, n *ivr.Node) (ivr.NodeResult, error) { return d.r(n) }
func (d *richDriver) Hangup(_ context.Context, n *ivr.Node) (ivr.NodeResult, error)   { return d.r(n) }

func TestWalkRecordsRichSteps(t *testing.T) {
	// menu(digit:1) → transfer(no_answer) → transfer(completed) → end
	flow := &ivr.Flow{
		EntryNode: "m1",
		Nodes: []ivr.Node{
			{ID: "m1", Type: ivr.NodeMenu},
			{ID: "t1", Type: ivr.NodeTransfer},
			{ID: "t2", Type: ivr.NodeTransfer},
		},
		Edges: []ivr.Edge{
			{From: "m1", To: "t1", Condition: "digit:1"},
			{From: "t1", To: "t2", Condition: "no_answer"},
		},
	}
	d := &richDriver{results: map[string]ivr.NodeResult{
		"m1": {Outcome: "digit:1", Action: "prompt_dtmf", DTMF: "1"},
		"t1": {Outcome: "no_answer", Action: "transfer", Detail: "alice"},
		"t2": {Outcome: "completed", Action: "transfer", Detail: "bob"},
	}}
	path, err := ivr.Walk(context.Background(), flow, "m1", d)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	want := []ivr.Step{
		{NodeID: "m1", NodeType: "menu", Action: "prompt_dtmf", DTMFInput: "1", Outcome: "digit:1"},
		{NodeID: "t1", NodeType: "transfer", Action: "transfer", Detail: "alice", Outcome: "no_answer"},
		{NodeID: "t2", NodeType: "transfer", Action: "transfer", Detail: "bob", Outcome: "completed"},
	}
	if len(path) != len(want) {
		t.Fatalf("steps = %d, want %d (%+v)", len(path), len(want), path)
	}
	for i, w := range want {
		if path[i] != w {
			t.Errorf("step %d = %+v, want %+v", i, path[i], w)
		}
	}
}

// clockedDriver exposes Now(), so Walk stamps each step's At. It returns a
// scripted advancing clock for deterministic assertions.
type clockedDriver struct {
	scriptDriver
	times []time.Time
	i     int
}

func (d *clockedDriver) Now() time.Time {
	t := d.times[d.i]
	if d.i < len(d.times)-1 {
		d.i++
	}
	return t
}

func TestWalkStampsStepTimesWhenDriverClocked(t *testing.T) {
	base := time.Date(2026, 7, 15, 7, 28, 15, 0, time.UTC)
	d := &clockedDriver{
		scriptDriver: scriptDriver{outcomes: map[string]string{"g": "default", "m": "digit:1", "t": "completed"}},
		times:        []time.Time{base, base.Add(2 * time.Second), base.Add(5 * time.Second), base.Add(9 * time.Second), base.Add(20 * time.Second)},
	}
	path, err := ivr.Walk(context.Background(), sampleFlow(), "s", d)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for i, s := range path {
		if s.At == nil {
			t.Fatalf("step %d (%s) has nil At — expected a stamped time", i, s.NodeID)
		}
	}
	if !path[0].At.Equal(base) {
		t.Fatalf("first step At = %v, want %v", path[0].At, base)
	}
	if !path[1].At.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("second step At = %v, want %v", path[1].At, base.Add(2*time.Second))
	}
}

func TestWalkLeavesStepTimesNilWhenDriverUnclocked(t *testing.T) {
	d := &scriptDriver{outcomes: map[string]string{"g": "default", "m": "digit:1", "t": "completed"}}
	path, err := ivr.Walk(context.Background(), sampleFlow(), "s", d)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for i, s := range path {
		if s.At != nil {
			t.Fatalf("step %d (%s) At = %v, want nil for an unclocked driver", i, s.NodeID, s.At)
		}
	}
}
