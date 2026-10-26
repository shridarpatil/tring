package signaling

// Graceful shutdown. On SIGTERM cmd/gateway calls Drain: new calls are turned
// away (a fresh web join into an empty room, an inbound phone call, a
// two-number connect) while the calls already up run to their end. Joins into
// a live call (an agent answering a ring, a page-refresh reattach) still work.

import (
	"context"
	"errors"
	"time"
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
