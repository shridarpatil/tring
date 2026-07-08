package signaling

import (
	"context"
	"testing"

	"github.com/shridarpatil/tring/internal/media/room"
)

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
