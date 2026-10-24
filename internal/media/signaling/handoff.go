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
// resumeHandoffIfAgentLeft. A phone caller is the room's port, not a
// participant: phone is set and caller is empty.
type handoff struct {
	caller room.ParticipantID
	phone  bool
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

// voidHandoff drops a pending transfer handoff WITHOUT resuming the paused IVR.
// Used when the answering agent initiates a blind transfer: their departure is a
// transfer (routeEvent must take the transferActive branch and keep the room for
// the new ring), NOT a normal handoff-end that should resume the IVR to a post-call
// node — resuming would walk the flow to completion and tear the caller down. The
// blocked AwaitHandoffEnd stays parked until the call's ctx cancels at teardown.
// The caller mapping is left intact.
func (s *Server) voidHandoff(roomID room.RoomID) {
	s.mu.Lock()
	delete(s.handoffs, roomID)
	s.mu.Unlock()
}

// AwaitHandoffEnd implements brain.HandoffWaiter. Called after an agent answers a
// transfer, it blocks until the agent leaves while the caller is still connected
// (returns true → the flow continues to the caller) or the call ends / the caller
// leaves / ctx is cancelled (returns false → the flow stops). For an untracked
// caller it returns true immediately, preserving the legacy "continue the moment
// an agent answers" behaviour.
func (s *Server) AwaitHandoffEnd(ctx context.Context, roomID string) bool {
	rid := room.RoomID(roomID)

	s.mu.Lock()
	caller, ok := s.callers[rid]
	_, phone := s.pstnCalls[rid]
	if !ok && !phone {
		s.mu.Unlock()
		return true // no tracked caller → legacy behaviour
	}
	resume := make(chan struct{})
	s.handoffs[rid] = &handoff{caller: caller, phone: phone, resume: resume}
	s.mu.Unlock()

	if phone {
		s.sfu.ReleasePortFeed(rid) // the phone leg hears the agent, not the idle IVR
	}

	defer func() {
		s.mu.Lock()
		if h := s.handoffs[rid]; h != nil && h.resume == resume {
			delete(s.handoffs, rid)
		}
		s.mu.Unlock()
	}()

	// Backstop the race where the agent hangs up before we registered the handoff:
	// resolve from the current membership instead of waiting for an event we missed.
	if done, callerPresent := s.handoffSettled(rid, caller, phone); done {
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
// and AwaitHandoffEnd should wait for the leave event. A phone room whose agent
// already left was torn down by routeEvent (nothing to resume), so it stops.
func (s *Server) handoffSettled(rid room.RoomID, caller room.ParticipantID, phone bool) (done, callerPresent bool) {
	r, ok := s.rooms.Get(rid)
	if phone {
		if !ok || len(r.Participants()) == 0 {
			return true, false
		}
		return false, true
	}
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
// been removed from the room's membership by the time this runs. A phone caller
// is still on the line while its leg is live; it resumes once no agent remains.
func (s *Server) resumeHandoffIfAgentLeft(roomID room.RoomID, left room.ParticipantID) bool {
	s.mu.Lock()
	h := s.handoffs[roomID]
	if h == nil || (!h.phone && left == h.caller) {
		delete(s.handoffs, roomID)
		s.mu.Unlock()
		return false
	}
	delete(s.handoffs, roomID)
	_, phoneLive := s.pstnCalls[roomID]
	s.mu.Unlock()

	r, ok := s.rooms.Get(roomID)
	if h.phone {
		if phoneLive && (!ok || len(r.Participants()) == 0) {
			close(h.resume)
			return true
		}
		return false
	}
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
