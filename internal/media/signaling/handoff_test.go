package signaling

import (
	"context"
	"testing"
	"time"

	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/media/sfu"
)

func newHandoffServer(t *testing.T) *Server {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	return New(Config{SFU: s})
}

// waitHandoff blocks until a handoff is registered for the room (or fails).
func waitHandoff(t *testing.T, s *Server, rid room.RoomID) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := s.handoffs[rid] != nil
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("handoff was never registered")
}

// When the answered agent leaves with the caller still present, AwaitHandoffEnd
// resumes (returns true) so the flow continues to the caller — and the caller is
// NOT ended (resumeHandoffIfAgentLeft handled it).
func TestAwaitHandoffEndResumesWhenAgentLeaves(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/call")
	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	s.setCaller(rid, caller.ID())
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	got := make(chan bool, 1)
	go func() { got <- s.AwaitHandoffEnd(context.Background(), string(rid)) }()
	waitHandoff(t, s, rid)

	agent.Leave() // emits ParticipantLeft → routeEvent → resume

	select {
	case v := <-got:
		if !v {
			t.Fatal("AwaitHandoffEnd = false, want true (agent left, caller present)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitHandoffEnd did not return after the agent left")
	}
	// caller must still be in the room (not ended by endIfAlone).
	if r, ok := s.rooms.Get(rid); !ok || len(r.Participants()) != 1 {
		t.Fatal("caller was ended; expected it to remain for the post-call flow")
	}
}

// When the caller leaves, the handoff is cleared (not resumed); AwaitHandoffEnd
// returns false once the call's context is cancelled (mirrors IVR teardown), so
// the flow stops instead of playing a post-call node to an empty room.
func TestAwaitHandoffEndStopsWhenCallerLeaves(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/call2")
	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	s.setCaller(rid, caller.ID())
	if _, err := s.rooms.Join(rid, "agent"); err != nil {
		t.Fatalf("join agent: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan bool, 1)
	go func() { got <- s.AwaitHandoffEnd(ctx, string(rid)) }()
	waitHandoff(t, s, rid)

	caller.Leave() // caller hung up → handoff voided, NOT resumed

	// the handoff must be gone and resume must not have fired.
	s.mu.Lock()
	h := s.handoffs[rid]
	s.mu.Unlock()
	if h != nil {
		t.Fatal("handoff not cleared after the caller left")
	}
	select {
	case v := <-got:
		t.Fatalf("AwaitHandoffEnd returned %v before teardown; should still be waiting", v)
	case <-time.After(50 * time.Millisecond):
	}

	cancel() // IVR teardown cancels the walk ctx
	select {
	case v := <-got:
		if v {
			t.Fatal("AwaitHandoffEnd = true, want false (caller gone)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitHandoffEnd did not return after ctx cancel")
	}
}
