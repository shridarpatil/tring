package signaling

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/ivrdrive"
	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/presence"
	"github.com/whatomate/tring/internal/queuekeys"
)

const (
	// ringPoll is how often Ring re-checks agent availability while a caller
	// waits in queue (so a freed-up agent is picked up promptly).
	ringPoll = 2 * time.Second
	// ringMaxWait bounds how long a caller waits (queue + ring) before the
	// transfer gives up with no_answer. Mirrors the FreeSWITCH path's default
	// team max_wait so the web and phone paths behave alike.
	ringMaxWait = 2 * time.Minute
)

// handleAgentPresence runs an agent's presence socket: it registers the agent as
// available, then blocks reading (keepalive / close) while it can receive
// `incoming` pushes from a transfer ring. The agent answers by opening a normal
// `join` on another socket. Deregisters on disconnect.
// setAgentOnCall tracks an agent's active call legs and reflects busy/available
// in the shared presence store the dashboard reads. The count handles an agent
// briefly on two legs (e.g. a transfer overlap) without flapping to available.
func (s *Server) setAgentOnCall(tenant, sip string, on bool) {
	if sip == "" {
		return
	}
	k := tenant + "|" + sip
	s.mu.Lock()
	if on {
		s.onCall[k]++
	} else if s.onCall[k] > 0 {
		s.onCall[k]--
	}
	n := s.onCall[k]
	if n == 0 {
		delete(s.onCall, k)
	}
	s.mu.Unlock()
	if pres := s.cfg.Presence; pres != nil {
		if n > 0 {
			pres.MarkOnCall(context.Background(), tenant, sip)
		} else {
			pres.MarkOnline(context.Background(), tenant, sip)
		}
	}
}

// agentOnCall reports whether the agent has any active call leg (used by the
// presence refresh so a 10s TTL re-assert doesn't clobber on_call → online).
func (s *Server) agentOnCall(tenant, sip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onCall[tenant+"|"+sip] > 0
}

func (s *Server) handleAgentPresence(c *conn, tenant, sip string) {
	s.mu.Lock()
	if s.agents[tenant] == nil {
		s.agents[tenant] = make(map[string]*conn)
	}
	s.agents[tenant][sip] = c
	s.mu.Unlock()
	_ = c.send(msg{Type: msgReady})
	s.log.Info("agent online", "tenant", tenant, "sip", sip)
	// Tell the brain (it owns routing/presence in 2c).
	if s.controlUp() {
		_ = s.control.Send(control.Message{Type: control.EvAgentOnline, Tenant: tenant, Sip: sip})
	}
	// Publish to the shared Redis presence store the API + admin console read, and
	// refresh its TTL while the socket is up (mirrors the legacy /agent/ws).
	if pres := s.cfg.Presence; pres != nil {
		pres.MarkOnline(context.Background(), tenant, sip)
		stopRefresh := make(chan struct{})
		go func() {
			t := time.NewTicker(presence.RefreshInterval)
			defer t.Stop()
			for {
				select {
				case <-stopRefresh:
					return
				case <-t.C:
					// Preserve on_call across TTL re-asserts — don't flip a busy
					// agent back to available every 10s.
					if s.agentOnCall(tenant, sip) {
						pres.MarkOnCall(context.Background(), tenant, sip)
					} else {
						pres.MarkOnline(context.Background(), tenant, sip)
					}
				}
			}
		}()
		defer func() {
			close(stopRefresh)
			pres.Offline(context.Background(), tenant, sip)
		}()
	}

	defer func() {
		s.mu.Lock()
		if s.agents[tenant][sip] == c {
			delete(s.agents[tenant], sip)
		}
		s.mu.Unlock()
		_ = c.Close()
		s.log.Info("agent offline", "tenant", tenant, "sip", sip)
		if s.controlUp() {
			_ = s.control.Send(control.Message{Type: control.EvAgentOffline, Tenant: tenant, Sip: sip})
		}
	}()

	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		if _, err := c.read(); err != nil {
			return
		}
		// Presence socket only takes keepalives; calls happen on a join socket.
	}
}

// Ring implements ivrdrive.Agents: it rings matching agents into `room` and
// blocks until one joins (a call `join` to that room closes the pending-ring
// channel → "completed") or ringMaxWait elapses ("no_answer"). `room` is the
// tenant-namespaced id ("<tenant>/<name>").
//
// Two behaviours matter here (both were bugs before):
//   - A busy agent (already on_call) is NOT rung — otherwise a second call pops
//     an incoming-call UI on someone mid-call.
//   - When every matching agent is busy, the caller WAITS in queue (re-checking
//     each tick) instead of immediately failing to no_answer — they connect as
//     soon as an agent frees up, like the FreeSWITCH team-queue path.
func (s *Server) Ring(ctx context.Context, roomID string, t ivrdrive.Target) (string, error) {
	tenant, name, ok := strings.Cut(roomID, "/")
	if !ok {
		return "no_answer", nil
	}

	// Resolve which SIPs to ring (DB lookup OUTSIDE the registry lock):
	//   - a specific agent_sip → just that agent
	//   - a team → its members (parity with the FreeSWITCH executor)
	//   - neither → every online agent of the tenant (ring-all)
	var sips []string
	ringAll := true
	if t.AgentSIP != "" {
		sips, ringAll = []string{t.AgentSIP}, false
	} else if t.Team != "" && s.cfg.Teams != nil {
		ringAll = false
		if tid, err := uuid.Parse(tenant); err == nil {
			if m, err := s.cfg.Teams.TeamSIPs(ctx, tid, t.Team); err == nil {
				sips = m
			} else {
				s.log.Warn("transfer: team resolve failed", "team", t.Team, "err", err)
			}
		}
	}
	// A targeted transfer (specific agent or a team) with no resolvable members
	// can never ring anyone — don't park the caller in a pointless queue.
	if !ringAll && len(sips) == 0 {
		return "no_answer", nil
	}

	// Register the pending ring up front (once) so an agent's join — which closes
	// this channel via signalRing — completes the transfer no matter which queue
	// iteration we're on, and can't race ahead of the waiter.
	rid := room.RoomID(roomID)
	ring := make(chan struct{})
	s.mu.Lock()
	s.rings[rid] = ring
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.rings[rid] == ring {
			delete(s.rings, rid)
		}
		s.mu.Unlock()
	}()

	// Dashboard queue depth: count this caller as waiting while no agent is free,
	// but only for a team transfer (the dashboard is per-team; ring-all and direct
	// agent_sip transfers have no team to attribute the wait to). Published to the
	// shared Redis the API sums with the phone side.
	trackDepth := t.Team != "" && s.cfg.Cache != nil
	dk := depthKey{tenant: tenant, team: t.Team}
	inQueue := false
	defer func() {
		if trackDepth && inQueue {
			s.addQueueDepth(dk, -1)
			s.publishQueueDepth(context.Background(), dk)
		}
	}()

	deadline := time.Now().Add(ringMaxWait)
	rung := map[string]bool{} // sips already shown an incoming-call popup this transfer
	queued := false

	for {
		// Candidates: online sockets not on a LOCAL web call (fast in-memory
		// filter under the registry lock — keep Redis I/O out of the lock).
		s.mu.Lock()
		cand := make(map[string]*conn)
		if ringAll {
			for sip, c := range s.agents[tenant] {
				if s.onCall[tenant+"|"+sip] == 0 {
					cand[sip] = c
				}
			}
		} else {
			for _, sip := range sips {
				if c := s.agents[tenant][sip]; c != nil && s.onCall[tenant+"|"+sip] == 0 {
					cand[sip] = c
				}
			}
		}
		s.mu.Unlock()

		// Cross-plane busy filter: also exclude any agent the SHARED presence store
		// marks on_call — a phone call on the FreeSWITCH plane (or another gateway
		// instance) that this process's in-memory onCall can't see. This is what
		// stops a web and a phone call both landing on the same agent.
		avail := cand
		if pres := s.cfg.Presence; pres != nil && pres.Enabled() {
			avail = make(map[string]*conn, len(cand))
			for sip, c := range cand {
				if pres.Available(ctx, tenant, sip) {
					avail[sip] = c
				}
			}
		}

		// Ring any available agent we haven't already popped this transfer (so a
		// still-ringing agent isn't re-popped every tick).
		for sip, c := range avail {
			if !rung[sip] {
				_ = c.send(msg{Type: msgIncoming, Room: name, From: t.Label})
				rung[sip] = true
			}
		}
		if len(avail) == 0 && !queued {
			queued = true
			s.log.Info("transfer: all matching agents busy — caller queued",
				"tenant", tenant, "team", t.Team, "agent", t.AgentSIP)
		}
		// Maintain the team's web queue depth: enter on no-availability, leave the
		// moment an agent frees up (gets rung). Re-publish each tick to refresh the
		// TTL and reflect the live count.
		if trackDepth {
			switch {
			case len(avail) == 0 && !inQueue:
				inQueue = true
				s.addQueueDepth(dk, +1)
			case len(avail) > 0 && inQueue:
				inQueue = false
				s.addQueueDepth(dk, -1)
			}
			s.publishQueueDepth(ctx, dk)
		}

		select {
		case <-ring:
			return "completed", nil
		case <-time.After(ringPoll):
			if time.Now().After(deadline) {
				return "no_answer", nil
			}
			// Forget agents that are no longer available (went busy/offline) so
			// they get a fresh popup if they free up again before the deadline.
			for sip := range rung {
				if _, ok := avail[sip]; !ok {
					delete(rung, sip)
				}
			}
		case <-ctx.Done():
			return "no_answer", ctx.Err()
		}
	}
}

// signalRing completes a pending transfer ring for a room (an agent joined).
func (s *Server) signalRing(roomID room.RoomID) {
	s.mu.Lock()
	ring := s.rings[roomID]
	if ring != nil {
		delete(s.rings, roomID)
	}
	s.mu.Unlock()
	if ring != nil {
		close(ring)
	}
}

// depthKey identifies a team's web-side queue (per tenant) for depth tracking.
type depthKey struct{ tenant, team string }

// addQueueDepth adjusts the live count of web callers queued for a team. The map
// entry is dropped at zero so a stale team never lingers.
func (s *Server) addQueueDepth(dk depthKey, d int) {
	s.qmu.Lock()
	s.queueDepth[dk] += d
	if s.queueDepth[dk] <= 0 {
		delete(s.queueDepth, dk)
	}
	s.qmu.Unlock()
}

// publishQueueDepth writes the team's current web queue depth to Redis under the
// "web" source key so the API sums it with the orchestrator's phone depth.
// Best-effort; no-op without a cache.
func (s *Server) publishQueueDepth(ctx context.Context, dk depthKey) {
	if s.cfg.Cache == nil {
		return
	}
	s.qmu.Lock()
	n := s.queueDepth[dk]
	s.qmu.Unlock()
	s.cfg.Cache.Set(ctx, queuekeys.DepthKey(dk.tenant, dk.team, queuekeys.SourceWeb),
		[]byte(strconv.Itoa(n)), queuekeys.DepthTTL)
}

// ensure the server satisfies the IVR's agent-ring seam.
var _ ivrdrive.Agents = (*Server)(nil)
