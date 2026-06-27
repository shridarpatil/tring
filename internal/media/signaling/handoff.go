package signaling

import (
	"context"

	"github.com/shridarpatil/tring/internal/media/room"
)

// handoff tracks a room where an agent has answered a transfer and the IVR is
// paused, waiting for the bridged call to end before continuing the flow. When
// the answered agent leaves with the caller still present, resume is closed so
// AwaitHandoffEnd returns true and a node wired to the transfer's "completed"
// edge runs to the caller (e.g. a post-call survey). See AwaitHandoffEnd +
// resumeHandoffIfAgentLeft.
type handoff struct {
	caller room.ParticipantID
	resume chan struct{}
}

// setCaller records the IVR caller's participant id for a room (the flow-pinned
// joiner), so a later transfer handoff can tell the caller apart from the agent
// that answers. Overwrites on reattach (same participant id).
func (s *Server) setCaller(roomID room.RoomID, pid room.ParticipantID) {
	s.mu.Lock()
	s.callers[roomID] = pid
	s.mu.Unlock()
}

// clearCaller drops the caller + any pending handoff for a room (on IVR teardown).
func (s *Server) clearCaller(roomID room.RoomID) {
	s.mu.Lock()
	delete(s.callers, roomID)
	delete(s.handoffs, roomID)
	s.mu.Unlock()
}

// AwaitHandoffEnd implements brain.HandoffWaiter. Called after an agent answers a
// transfer, it blocks until the agent leaves while the caller is still connected
// (returns true → the flow continues to the caller) or the call ends / the caller
// leaves / ctx is cancelled (returns false → the flow stops). For a phone room (a
// port caller) or an untracked caller it returns true immediately, preserving the
// legacy "continue the moment an agent answers" behaviour.
func (s *Server) AwaitHandoffEnd(ctx context.Context, roomID string) bool {
	rid := room.RoomID(roomID)
	if s.sfu.HasPort(rid) {
		return true // phone plug: no web post-call handling yet
	}

	s.mu.Lock()
	caller, ok := s.callers[rid]
	if !ok {
		s.mu.Unlock()
		return true // no tracked caller → legacy behaviour
	}
	resume := make(chan struct{})
	s.handoffs[rid] = &handoff{caller: caller, resume: resume}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if h := s.handoffs[rid]; h != nil && h.resume == resume {
			delete(s.handoffs, rid)
		}
		s.mu.Unlock()
	}()

	// Backstop the race where the agent hangs up before we registered the handoff:
	// resolve from the current membership instead of waiting for an event we missed.
	if done, callerPresent := s.handoffSettled(rid, caller); done {
		return callerPresent
	}

	select {
	case <-resume:
		return true
	case <-ctx.Done():
		return false
	}
}

// handoffSettled reports whether the bridged call is already down to just the
// caller (done, callerPresent=true → resume) or the caller is already gone
// (done, callerPresent=false → stop). done=false means an agent is still present
// and AwaitHandoffEnd should wait for the leave event.
func (s *Server) handoffSettled(rid room.RoomID, caller room.ParticipantID) (done, callerPresent bool) {
	r, ok := s.rooms.Get(rid)
	if !ok {
		return true, false
	}
	present, others := false, 0
	for _, p := range r.Participants() {
		if p.ID() == caller {
			present = true
		} else {
			others++
		}
	}
	if !present {
		return true, false // caller gone
	}
	if others == 0 {
		return true, true // only the caller remains → agent already left
	}
	return false, true // agent still present → wait
}

// resumeHandoffIfAgentLeft handles a participant leaving a room with a pending
// transfer handoff. If the leaver is the answered agent and the caller is still
// present, it resumes the IVR (closing resume) and returns true so the caller is
// NOT ended. Otherwise (the caller left, or no handoff) it clears any handoff and
// returns false so normal teardown proceeds. The leaving participant has already
// been removed from the room's membership by the time this runs.
func (s *Server) resumeHandoffIfAgentLeft(roomID room.RoomID, left room.ParticipantID) bool {
	s.mu.Lock()
	h := s.handoffs[roomID]
	if h == nil || left == h.caller {
		delete(s.handoffs, roomID)
		s.mu.Unlock()
		return false
	}
	delete(s.handoffs, roomID)
	s.mu.Unlock()

	r, ok := s.rooms.Get(roomID)
	if !ok {
		return false
	}
	for _, p := range r.Participants() {
		if p.ID() == h.caller {
			close(h.resume)
			return true
		}
	}
	return false
}
