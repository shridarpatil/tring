package signaling

// ivrdial.go — IVR transfer-to-number: a flow's transfer node dials a phone
// number (often one an http_callback returned) and, once it answers, joins it
// into the caller's room as a port — the same attach an agent-initiated dial
// uses (outbound.go). The callee then stands in for an agent: the caller and
// callee hear each other while the IVR waits. The caller hanging up ends both;
// the callee hanging up resumes the flow to the caller (the transfer's
// "completed" edge), which ends the call if nothing is wired there.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/room"
)

// ivrDialer adapts the server to ivrdrive.Dialer.
type ivrDialer struct{ s *Server }

func (d ivrDialer) Dial(ctx context.Context, roomID, number, callerID string, ring time.Duration, answered func()) (string, error) {
	return d.s.dialFromIVR(ctx, room.RoomID(roomID), number, callerID, ring, answered)
}

// dialFromIVR rings number for up to ring and, on answer, runs answered (stop
// the ringing music), attaches the callee to roomID and blocks until that call
// ends. callerID ("" = the trunk's own DID) must be one of the tenant's numbers.
// ctx is the IVR walk's: it ends when the caller leaves, which stops the ring.
// Any failure (invalid number, no trunk, busy, no answer) is "no_answer" so the
// flow's fallback branch runs; errors are only logged. Once answered it is
// "completed" when the callee hangs up with the caller still on the line, or
// ("completed", ivrdrive.ErrCallEnded) when the caller is gone.
func (s *Server) dialFromIVR(ctx context.Context, roomID room.RoomID, number, callerID string, ring time.Duration, answered func()) (string, error) {
	tenantStr, _, _ := strings.Cut(string(roomID), "/")
	tenant, err := uuid.Parse(tenantStr)
	if err != nil {
		s.log.Warn("ivr dial: room has no tenant", "room", roomID)
		return "no_answer", nil
	}

	// The answered dialog can't ride ctx: the caller leaving must hang it up
	// through the teardown paths (which settle the CDR first), not by a context.
	// Give it its own, cancelled by the ring timeout or the caller leaving — but
	// only until it answers.
	octx, ocancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(ring, ocancel)
	stopWatch := context.AfterFunc(ctx, ocancel)
	leg, callerID, to, err := s.originateOut(octx, tenant, number, callerID, nil)
	timer.Stop()
	stopWatch()
	if err != nil {
		ocancel()
		s.log.Info("ivr dial: not answered", "room", roomID, "to", to, "err", err)
		return "no_answer", nil
	}
	pid := leg.Source.ID()
	if ctx.Err() != nil {
		// The caller hung up while the callee rang, and the callee answered anyway.
		ocancel()
		s.log.Info("ivr dial: caller left during ring, hanging up callee", "room", roomID)
		_ = s.cfg.Originator.HangupOutbound(pid)
		return "no_answer", nil
	}
	if answered != nil {
		answered() // the caller hears the callee, not the ringing music
	}
	if err := s.sfu.AttachPort(roomID, leg.Source, leg.Write); err != nil {
		ocancel()
		s.log.Error("ivr dial: attach port failed", "room", roomID, "err", err)
		_ = s.cfg.Originator.HangupOutbound(pid)
		return "no_answer", nil
	}

	s.mu.Lock()
	s.outboundLegs[roomID] = pid
	s.mu.Unlock()

	// The callee answering is the call's "answered" moment, as an agent joining
	// would be: stamps the CDR, fires call.answered, and records the callee as the
	// agent side of the stereo recording.
	if s.cfg.Calls != nil {
		if sink := s.cfg.Calls.Answered(string(roomID), pid, ""); sink != nil {
			s.sfu.AddSink(roomID, pid, sink)
		}
	}
	// A phone caller's feed is still the IVR that played the ringing music.
	s.sfu.ReleasePortFeed(roomID)
	s.log.Info("ivr dial: callee connected", "room", roomID, "to", to, "caller_id", callerID)

	resume := make(chan bool, 1) // true: the callee hung up, the caller is still on the line
	go func() {
		<-leg.Done
		ocancel()
		if ctx.Err() == nil && s.callerOnLine(roomID) {
			s.dropIVRCallee(roomID, pid)
			resume <- true
			return
		}
		s.endOutboundRoom(roomID, "callee_hangup")
		s.hangupPstnIfAbandoned(roomID) // a phone caller has nobody left to talk to
		resume <- false
	}()
	select {
	case ok := <-resume:
		if ok {
			return "completed", nil
		}
	case <-ctx.Done():
		// The caller left; the teardown that cancelled ctx hangs up the callee.
	}
	return "completed", ivrdrive.ErrCallEnded
}

// callerOnLine reports whether an IVR caller is still connected: a live phone
// leg, or a web caller still in the room.
func (s *Server) callerOnLine(roomID room.RoomID) bool {
	if s.phoneLive(roomID) {
		return true
	}
	r, ok := s.rooms.Get(roomID)
	return ok && len(r.Participants()) > 0
}

// dropIVRCallee removes a dialed callee who hung up while the caller stays on
// for the rest of the flow. The callee ended the conversation, so they stay who
// hung up when the flow later ends the call.
func (s *Server) dropIVRCallee(roomID room.RoomID, pid room.ParticipantID) {
	s.mu.Lock()
	delete(s.outboundLegs, roomID)
	s.mu.Unlock()
	s.sfu.DetachPort(roomID, pid)
	s.setHangupBy(roomID, "callee_hangup")
	s.log.Info("ivr dial: callee hung up, resuming the flow", "room", roomID)
}
