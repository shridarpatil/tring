package brain

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/whatomate/tring/internal/ivr"
)

// blockingDriver walks start→transfer; Transfer blocks until ctx is cancelled,
// modelling a caller held in queue who then abandons (Stop cancels the walk).
type blockingDriver struct{ dtmf chan string }

func (d *blockingDriver) Greeting(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	return ivr.NodeResult{Outcome: "default", Action: "play_audio"}, nil
}
func (d *blockingDriver) Menu(context.Context, *ivr.Node) (ivr.NodeResult, error)    { return ivr.NodeResult{}, nil }
func (d *blockingDriver) Gather(context.Context, *ivr.Node) (ivr.NodeResult, error)  { return ivr.NodeResult{}, nil }
func (d *blockingDriver) Transfer(ctx context.Context, n *ivr.Node) (ivr.NodeResult, error) {
	<-ctx.Done()
	return ivr.NodeResult{Action: "transfer", Outcome: ""}, nil // terminal on abandon
}
func (d *blockingDriver) HTTPCallback(context.Context, *ivr.Node) (ivr.NodeResult, error) { return ivr.NodeResult{}, nil }
func (d *blockingDriver) Timing(context.Context, *ivr.Node) (ivr.NodeResult, error)       { return ivr.NodeResult{}, nil }
func (d *blockingDriver) GotoFlow(context.Context, *ivr.Node) (ivr.NodeResult, error)     { return ivr.NodeResult{}, nil }
func (d *blockingDriver) Hangup(context.Context, *ivr.Node) (ivr.NodeResult, error)       { return ivr.NodeResult{Outcome: ""}, nil }

func flowGreetThenTransfer() *ivr.Flow {
	return &ivr.Flow{
		EntryNode: "g",
		Nodes:     []ivr.Node{{ID: "g", Type: ivr.NodeGreeting}, {ID: "t", Type: ivr.NodeTransfer}},
		Edges:     []ivr.Edge{{From: "g", To: "t", Condition: "default"}},
	}
}

func TestRunnerStampsJourneyBeforeDone(t *testing.T) {
	r := NewRunner(nil)
	var mu sync.Mutex
	var order []string
	var steps []ivr.Step
	onJourney := func(s []ivr.Step) { mu.Lock(); order = append(order, "journey"); steps = s; mu.Unlock() }
	onDone := func() { mu.Lock(); order = append(order, "done"); mu.Unlock() }

	r.Start(context.Background(), "room-1", flowGreetThenTransfer(), &blockingDriver{}, onJourney, onDone)
	time.Sleep(50 * time.Millisecond) // let the walk reach the blocking Transfer
	r.Stop("room-1")
	r.Stop("room-1") // idempotent — must not stamp twice

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "journey" || order[1] != "done" {
		t.Fatalf("want [journey done], got %v", order)
	}
	hasTransfer := false
	for _, s := range steps {
		if s.NodeType == ivr.NodeTransfer {
			hasTransfer = true
		}
	}
	if !hasTransfer {
		t.Fatalf("journey missing transfer step: %+v", steps)
	}
}
