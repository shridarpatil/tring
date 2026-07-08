package signaling

import (
	"context"

	"github.com/shridarpatil/tring/internal/media/room"
)

// beginTransfer marks a room as having a blind transfer ringing and returns a
// context cancelled when the transfer ends (endTransfer) or the caller hangs up
// (routeEvent's empty-room branch). ok=false when a transfer is already in flight
// for the room (a second transfer is rejected).
func (s *Server) beginTransfer(rid room.RoomID) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.transfers[rid]; exists {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.transfers[rid] = cancel
	return ctx, true
}

// transferActive reports whether a blind transfer is ringing for the room. The
// caller is briefly alone during it, so routeEvent must not tear the room down.
func (s *Server) transferActive(rid room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.transfers[rid]
	return ok
}

// endTransfer clears the transfer guard and cancels its context. Idempotent —
// safe to call from both the transfer goroutine and the room-teardown path.
func (s *Server) endTransfer(rid room.RoomID) {
	s.mu.Lock()
	cancel := s.transfers[rid]
	delete(s.transfers, rid)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
