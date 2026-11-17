package signaling

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/routing"
)

// failingRenewals grants a reservation but fails every renewal, as a Redis
// error or timeout on the renew tick would.
type failingRenewals struct{}

func (failingRenewals) Reserve(context.Context, string, string, time.Duration) bool { return true }
func (failingRenewals) Renew(context.Context, string, string, time.Duration) bool   { return false }
func (failingRenewals) Release(context.Context, string, string)                     {}
func (failingRenewals) Reserved(context.Context, string) bool                       { return false }

// An agent already on a call keeps it when their reservation is lost: a failed
// renewal used to hang up answered (and transferred) calls.
func TestClaimLostKeepsAnsweredCall(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out one claim renewal tick (~10s)")
	}
	s := newOutboundServer(t, nil, nil)
	tenant := uuid.NewString()
	rid := room.RoomID(tenant + "/claim-lost")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	claim := routing.Acquire(context.Background(), failingRenewals{}, tenant, "alice")
	if claim == nil {
		t.Fatal("acquire claim")
	}
	t.Cleanup(claim.Release)
	s.holdAgentLeg(agent, tenant, "alice", claim)

	select {
	case <-claim.Lost():
	case <-time.After(15 * time.Second):
		t.Fatal("claim never reported lost")
	}
	time.Sleep(100 * time.Millisecond) // let the watcher react
	if agent.State() == room.StateLeft {
		t.Fatal("agent was removed from an answered call when its claim was lost")
	}
}
