package signaling

import (
	"context"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/room"
)

// prepareRing starts a transfer ring on rid and swaps its hold-music stop for a
// probe, so a test sees when the caller's hold music would stop.
func prepareRing(t *testing.T, s *Server, rid room.RoomID) (cleanup func(), stopped chan struct{}) {
	t.Helper()
	_, eff, cleanup := s.Prepare(context.Background(), string(rid), ivrdrive.Target{}, true, nil, "", "")
	stopped = make(chan struct{})
	eff.(*pionRing).holdStop = func() { close(stopped) }
	return cleanup, stopped
}

func isClosed(ch chan struct{}, within time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(within):
		return false
	}
}

// The caller keeps hearing hold music after the agent answers until the agent's
// media is actually up — not the instant the agent's browser joins the room.
func TestAnswerKeepsHoldUntilAgentMediaUp(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/answer-hold")
	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	caller.MarkConnected()
	cleanup, stopped := prepareRing(t, s, rid)

	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	s.signalRing(rid) // the agent answered
	cleanup()

	if isClosed(stopped, 100*time.Millisecond) {
		t.Fatal("hold music stopped before the agent's media was up (the caller hears silence)")
	}
	agent.MarkConnected()
	if !isClosed(stopped, time.Second) {
		t.Fatal("hold music kept playing after the agent's media came up")
	}
}

// No answer: the hold music stops right away.
func TestNoAnswerStopsHoldNow(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/answer-hold-2")
	cleanup, stopped := prepareRing(t, s, rid)
	cleanup()
	if !isClosed(stopped, 100*time.Millisecond) {
		t.Fatal("hold music kept playing after an unanswered ring")
	}
}

// An agent who answers and leaves before their media is up doesn't leave the
// caller on hold music until the wait runs out.
func TestAnswerThenAgentLeavesStopsHold(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/answer-hold-3")
	cleanup, stopped := prepareRing(t, s, rid)
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	s.signalRing(rid)
	cleanup()
	agent.Leave()
	if !isClosed(stopped, time.Second) {
		t.Fatal("hold music kept playing after the answering agent left")
	}
}
