package signaling

import (
	"context"
	"testing"

	"github.com/shridarpatil/tring/internal/media/room"
)

// While a transfer is active, an agent leaving must NOT end the lone caller:
// routeEvent's endIfAlone branch must be suppressed so the caller stays on the
// line while the transfer target rings (or the no-answer message plays).
func TestTransferGuardSuppressesAloneTeardown(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/guard")
	if _, err := s.rooms.Join(rid, "caller"); err != nil {
		t.Fatalf("join caller: %v", err)
	}
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	if _, ok := s.beginTransfer(rid); !ok {
		t.Fatal("beginTransfer should succeed")
	}
	agent.Leave() // fires routeEvent EventParticipantLeft synchronously

	// The room must still hold the caller (guard prevented endIfAlone).
	r, ok := s.rooms.Get(rid)
	if !ok || len(r.Participants()) != 1 {
		t.Fatalf("caller should remain during transfer; ok=%v room=%v", ok, r)
	}
}

func TestTransferGuardLifecycle(t *testing.T) {
	s := &Server{transfers: map[room.RoomID]context.CancelFunc{}}
	rid := room.RoomID("t/r")

	if s.transferActive(rid) {
		t.Fatal("no transfer should be active initially")
	}
	ctx, ok := s.beginTransfer(rid)
	if !ok || ctx == nil {
		t.Fatal("beginTransfer should start a transfer")
	}
	if !s.transferActive(rid) {
		t.Fatal("transfer should be active after begin")
	}
	if _, ok := s.beginTransfer(rid); ok {
		t.Fatal("a second beginTransfer must be rejected (double transfer)")
	}
	s.endTransfer(rid)
	if s.transferActive(rid) {
		t.Fatal("transfer should be inactive after end")
	}
	if ctx.Err() == nil {
		t.Fatal("endTransfer must cancel the transfer context")
	}
}
