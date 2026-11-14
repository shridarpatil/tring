package signaling

// Graceful shutdown. On SIGTERM cmd/tring calls Drain: new calls are turned
// away (a fresh web join into an empty room, an inbound phone call, a
// two-number connect) while the calls already up run to their end. Joins into
// a live call (an agent answering a ring, a page-refresh reattach) still work.

import (
	"context"
	"errors"
	"time"

	"github.com/shridarpatil/tring/internal/media/room"
)

// drainPoll is how often Drain re-counts the live calls.
const drainPoll = 250 * time.Millisecond

// errDraining: the gateway is shutting down and takes no new calls.
var errDraining = errors.New("gateway restarting")

// Drain stops new calls and blocks until every live call has ended or ctx is
// done. It returns the number of calls still up (0 = drained cleanly).
func (s *Server) Drain(ctx context.Context) int {
	s.draining.Store(true)
	s.log.Info("draining: no new calls", "calls", s.activeCalls())
	t := time.NewTicker(drainPoll)
	defer t.Stop()
	last := -1
	for {
		select {
		case <-ctx.Done():
			return s.activeCalls()
		case <-t.C:
		}
		n := s.activeCalls()
		if n == 0 {
			return 0
		}
		if n != last {
			s.log.Info("draining: waiting for calls to end", "calls", n)
			last = n
		}
	}
}

// EndAll ends every call still up — for when Drain timed out and the process
// is about to exit. Each call goes through the teardown a hangup would take, so
// its CDR settles, its recording is finalized, call.ended fires, queued callers
// leave their line and every carrier leg gets a BYE (instead of sitting in
// billed dead air until FreeSWITCH's RTP timeout). It waits, until ctx is done,
// for that teardown to finish and returns the calls still up.
func (s *Server) EndAll(ctx context.Context) int {
	ports := s.sfu.Ports()
	webRooms := s.rooms.IDs()

	// Settle every CDR as a system end first. Settling is idempotent, so the
	// hangups below can't record it as the caller or callee hanging up.
	for rid := range ports {
		s.endCallRecord(rid)
	}
	for _, rid := range webRooms {
		s.endCallRecord(rid)
	}

	// Carrier legs. An inbound phone call is hung up through the plug; its
	// teardown (endPstnRoom) also hangs up a number its IVR dialed. Every other
	// leg (click-to-call, connect and campaign legs) goes through the
	// originator. A leg that already ended is a no-op either way.
	s.mu.Lock()
	inbound := make(map[room.RoomID]room.CallID, len(s.pstnCalls))
	for rid, id := range s.pstnCalls {
		if _, campaign := s.campaignLegs[rid]; !campaign {
			inbound[rid] = id
		}
	}
	s.mu.Unlock()
	for _, id := range inbound {
		_ = s.pstnGW.Hangup(id)
	}
	for rid, pids := range ports {
		if _, ok := inbound[rid]; ok || s.cfg.Originator == nil {
			continue
		}
		for _, pid := range pids {
			_ = s.cfg.Originator.HangupOutbound(pid)
		}
	}

	// Browser participants: a terminal `ended` so the SDK doesn't reconnect,
	// then leave (which stops each room's IVR and ring).
	for _, rid := range webRooms {
		r, ok := s.rooms.Get(rid)
		if !ok {
			continue
		}
		for _, p := range r.Participants() {
			if c := s.connFor(p.ID()); c != nil {
				_ = c.send(msg{Type: msgEnded, Reason: errDraining.Error()})
			}
			s.removeParticipant(rid, p.ID())
		}
	}

	t := time.NewTicker(drainPoll)
	defer t.Stop()
	for {
		if n := s.activeCalls(); n == 0 {
			return 0
		}
		select {
		case <-ctx.Done():
			return s.activeCalls()
		case <-t.C:
		}
	}
}

// endCallRecord settles rid's CDR as a system end (a no-op without one).
func (s *Server) endCallRecord(rid room.RoomID) {
	s.setHangupBy(rid, "system")
	if s.cfg.Calls != nil {
		s.cfg.Calls.Ended(string(rid), "system")
	}
}

// activeCalls counts the live calls: web rooms (which include phone rooms an
// agent has joined) plus phone/connect calls running outside the room manager.
// A call can be counted twice; Drain only needs zero vs not.
func (s *Server) activeCalls() int {
	return s.rooms.Len() + int(s.portCalls.Load())
}

// startPortCall counts a phone/connect call for Drain. false → the gateway is
// draining and the call must be refused. Counting before checking means Drain
// can't see zero while a call slips in.
func (s *Server) startPortCall() bool {
	s.portCalls.Add(1)
	if s.draining.Load() {
		s.portCalls.Add(-1)
		return false
	}
	return true
}

func (s *Server) endPortCall() { s.portCalls.Add(-1) }
