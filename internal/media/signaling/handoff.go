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
	// outcome is the Transfer node outcome the flow resumes with, set before
	// resume closes: "" → "completed". A later agent transfer of the call
	// that nobody took sets its failure ("queue_timeout" / "no_answer").
	outcome string
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
// (resume=true → the flow continues to the caller, with outcome "completed") or
// the call ends / the caller leaves / ctx is cancelled (resume=false → the flow
// stops). When the agent hands the call on with a transfer that nobody takes,
// the flow resumes with that failure as the outcome instead (see ringTransfer).
// For an untracked caller it returns at once, preserving the legacy "continue
// the moment an agent answers" behaviour.
func (s *Server) AwaitHandoffEnd(ctx context.Context, roomID string) (outcome string, resume bool) {
	rid := room.RoomID(roomID)

	s.mu.Lock()
	caller, ok := s.callers[rid]
	_, phone := s.pstnCalls[rid]
	if !ok && !phone {
		s.mu.Unlock()
		return "completed", true // no tracked caller → legacy behaviour
	}
	h := &handoff{caller: caller, phone: phone, resume: make(chan struct{})}
	// The answering agent may already be transferring the call on (they can
	// act the moment they join, before this registers). Set the handoff aside
	// with that transfer, as commitTransfer does with one registered earlier:
	// the agent leaving for it must not read as the handoff ending.
	parked := false
	if pt := s.transferring[rid]; pt != nil && pt.handoff == nil {
		pt.handoff, parked = h, true
	} else {
		s.handoffs[rid] = h
	}
	s.mu.Unlock()

	if phone {
		s.sfu.ReleasePortFeed(rid) // the phone leg hears the agent, not the idle IVR
	}

	defer func() {
		s.mu.Lock()
		if s.handoffs[rid] == h {
			delete(s.handoffs, rid)
		}
		s.mu.Unlock()
	}()

	// Backstop the race where the agent hangs up before we registered the handoff:
	// resolve from the current membership instead of waiting for an event we missed.
	// (A handoff parked with a transfer is settled by that transfer instead.)
	if done, callerPresent := s.handoffSettled(rid, caller, phone); done && !parked {
		return "completed", callerPresent
	}

	select {
	case <-h.resume:
		if h.outcome != "" {
			return h.outcome, true
		}
		return "completed", true
	case <-ctx.Done():
		return "", false
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
// transfer handoff. Once the caller is left alone with it — every agent gone,
// the caller still on the line — it resumes the IVR (closing resume) and
// returns true so the caller is NOT ended. While another agent is still on the
// call (an agent transfer's target took over from the agent who just left) the
// handoff stays pending for that agent's leave. When the caller left, the
// handoff is dropped and it returns false so normal teardown proceeds. The
// leaving participant has already been removed from the room's membership by
// the time this runs. A phone caller is the room's port, not a participant: it
// is on the line while its leg is live.
func (s *Server) resumeHandoffIfAgentLeft(roomID room.RoomID, left room.ParticipantID) bool {
	s.mu.Lock()
	h := s.handoffs[roomID]
	if h == nil {
		s.mu.Unlock()
		return false
	}
	_, phoneLive := s.pstnCalls[roomID]
	s.mu.Unlock()

	r, ok := s.rooms.Get(roomID)
	callerPresent, others := false, 0
	if ok {
		for _, p := range r.Participants() {
			if !h.phone && p.ID() == h.caller {
				callerPresent = true
			} else {
				others++
			}
		}
	}
	if h.phone {
		callerPresent = phoneLive
	}
	if !h.phone && left == h.caller {
		callerPresent = false
	}
	resume := callerPresent && others == 0

	s.mu.Lock()
	if s.handoffs[roomID] != h {
		s.mu.Unlock() // resolved concurrently
		return false
	}
	if resume || !callerPresent {
		delete(s.handoffs, roomID)
	}
	s.mu.Unlock()
	if resume {
		close(h.resume)
	}
	return resume
}
