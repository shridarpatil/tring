package signaling

// ivrdial.go — IVR transfer-to-number: a flow's transfer node dials a phone
// number (often one an http_callback returned) and, once it answers, joins it
// into the caller's room as a port — the same attach an agent-initiated dial
// uses (outbound.go). The callee then stands in for an agent: the IVR leaves,
// the caller and callee hear each other, and either side hanging up ends both.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
)

// ivrDialer adapts the server to ivrdrive.Dialer.
type ivrDialer struct{ s *Server }

func (d ivrDialer) Dial(ctx context.Context, roomID, number, callerID string, ring time.Duration) (string, error) {
	return d.s.dialFromIVR(ctx, room.RoomID(roomID), number, callerID, ring)
}

// dialFromIVR rings number for up to ring and, on answer, attaches it to roomID.
// callerID ("" = the trunk's own DID) must be one of the tenant's numbers.
// ctx is the IVR walk's: it ends when the caller leaves, which stops the ring.
// Any failure (invalid number, no trunk, busy, no answer) is "no_answer" so the
// flow's fallback branch runs; errors are only logged.
func (s *Server) dialFromIVR(ctx context.Context, roomID room.RoomID, number, callerID string, ring time.Duration) (string, error) {
	tenantStr, _, _ := strings.Cut(string(roomID), "/")
	tenant, err := uuid.Parse(tenantStr)
	if err != nil {
		s.log.Warn("ivr dial: room has no tenant", "room", roomID)
		return "no_answer", nil
	}

	// The answered dialog outlives the IVR walk, so it can't ride ctx (the walk
	// ends right after "completed"). Give it its own context, cancelled by the
	// ring timeout or the caller leaving — but only until it answers.
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
	s.log.Info("ivr dial: callee connected", "room", roomID, "to", to, "caller_id", callerID)

	go func() {
		<-leg.Done
		ocancel()
		s.endOutboundRoom(roomID, "callee_hangup")
		s.hangupPstnIfAbandoned(roomID) // a phone caller has nobody left to talk to
	}()
	return "completed", nil
}
