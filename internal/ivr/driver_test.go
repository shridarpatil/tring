package ivr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/whatomate/tring/internal/ivr"
)

// scriptDriver is a fake CallDriver: each node-effect returns a scripted outcome
// (keyed by node id), so Walk's traversal can be tested deterministically with
// no FreeSWITCH/pion.
type scriptDriver struct {
	outcomes map[string]string // node id → outcome
	failOn   string            // node id to return an error from
}

func (d *scriptDriver) out(n *ivr.Node) (string, error) {
	if n.ID == d.failOn {
		return "", errors.New("boom")
	}
	return d.outcomes[n.ID], nil
}
func (d *scriptDriver) Greeting(_ context.Context, n *ivr.Node) (string, error)     { return d.out(n) }
func (d *scriptDriver) Menu(_ context.Context, n *ivr.Node) (string, error)         { return d.out(n) }
func (d *scriptDriver) Gather(_ context.Context, n *ivr.Node) (string, error)       { return d.out(n) }
func (d *scriptDriver) Transfer(_ context.Context, n *ivr.Node) (string, error)     { return d.out(n) }
func (d *scriptDriver) HTTPCallback(_ context.Context, n *ivr.Node) (string, error) { return d.out(n) }
func (d *scriptDriver) Timing(_ context.Context, n *ivr.Node) (string, error)       { return d.out(n) }
func (d *scriptDriver) GotoFlow(_ context.Context, n *ivr.Node) (string, error)     { return d.out(n) }
func (d *scriptDriver) Hangup(_ context.Context, _ *ivr.Node) (string, error)       { return "", nil }

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
