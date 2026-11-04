package brain

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/routing"
)

// pollEffects answers any ring, and holds by polling ready like the pion plane.
type pollEffects struct{}

func (pollEffects) Ring(context.Context, []string, time.Duration) (routing.RingOutcome, error) {
	return routing.RingOutcome{Answered: true}, nil
}
func (pollEffects) Hold(ctx context.Context, max time.Duration, ready func() bool) {
	end := time.Now().Add(max)
	for !ready() && ctx.Err() == nil && time.Now().Before(end) {
		time.Sleep(5 * time.Millisecond)
	}
}
func (pollEffects) Answered() (bool, string)         { return false, "" }
func (pollEffects) CallerAlive(context.Context) bool { return true }

// busyTransport offers every requested agent except the busy ones.
type busyTransport struct{ busy map[string]bool }

func (b busyTransport) Prepare(_ context.Context, _ string, _ ivrdrive.Target, _ bool, sips []string, _, _ string) (func(context.Context) []string, routing.Effects, func()) {
	return func(context.Context) []string {
		var out []string
		for _, s := range sips {
			if !b.busy[s] {
				out = append(out, s)
			}
		}
		return out
	}, pollEffects{}, func() {}
}

// A transfer waiting for a busy agent must not hold up a transfer to a free
// one. Direct-agent transfers have no team, and all queued on one tenant-wide
// line — prod 2026-11-04: a transfer to a free agent waited ~2 min behind one to
// a busy agent.
func TestDirectTransferNotBlockedByAnotherAgentsQueue(t *testing.T) {
	r := newRouter(busyTransport{busy: map[string]bool{"bob": true}})
	tenant := uuid.New().String()

	waitBob, stopBob := context.WithCancel(context.Background())
	defer stopBob()
	go r.RingNoHandoff(waitBob, tenant+"/call-1", ivrdrive.Target{AgentSIP: "bob"})
	time.Sleep(50 * time.Millisecond) // bob's caller is queued first

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, _ := r.RingNoHandoff(ctx, tenant+"/call-2", ivrdrive.Target{AgentSIP: "alice"}); out != "completed" {
		t.Fatalf("transfer to free alice = %q, want completed (blocked behind bob's queue?)", out)
	}
}
