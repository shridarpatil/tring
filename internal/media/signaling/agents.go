package signaling

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/ivrdrive"
	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/presence"
	"github.com/whatomate/tring/internal/queuekeys"
	"github.com/whatomate/tring/internal/routing"
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

// Ring implements ivrdrive.Agents by running the SHARED routing.Router
// queue/ring loop with a pion Effects adapter (pionRing). Agents are rung by
// pushing msgIncoming over their presence sockets; an agent joining the room
// closes the pending-ring channel (signalRing) → answered. For a team it honors
// the team's strategy / ring_seconds / max_wait identically to the FreeSWITCH
// plane; ring-all and direct-agent transfers use the routing defaults. `room` is
// the tenant-namespaced id ("<tenant>/<name>").
func (s *Server) Ring(ctx context.Context, roomID string, t ivrdrive.Target) (string, error) {
	tenant, name, ok := strings.Cut(roomID, "/")
	if !ok {
		return "no_answer", nil
	}

	// Resolve the target → candidate SIPs (DB lookup outside the registry lock):
	// a specific agent_sip → just that agent; a team → its members; neither →
	// every online agent of the tenant (ring-all).
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
	// A targeted transfer with no resolvable members can never ring anyone.
	if !ringAll && len(sips) == 0 {
		return "no_answer", nil
	}

	// candidates: current online agents not on a LOCAL web call (the registry's
	// instant busy view). routing.FilterAvailable then drops agents the shared
	// presence marks on_call and any the phone side has reserved.
	candidates := func(context.Context) []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []string
		if ringAll {
			for sip := range s.agents[tenant] {
				if s.onCall[tenant+"|"+sip] == 0 {
					out = append(out, sip)
				}
			}
		} else {
			for _, sip := range sips {
				if c := s.agents[tenant][sip]; c != nil && s.onCall[tenant+"|"+sip] == 0 {
					out = append(out, sip)
				}
			}
		}
		return out
	}

	// Pending-ring channel: signalRing closes it when an agent joins the room.
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

	// Per-team queue config (strategy / ring_seconds / max_wait) + depth source —
	// only for a genuinely resolved team (!ringAll implies a non-nil Teams
	// resolver and resolved members); ring-all / direct-agent use the defaults.
	rc := routing.RouteConfig{TenantID: tenant}
	if !ringAll && t.Team != "" {
		rc.Team, rc.Source = t.Team, queuekeys.SourceWeb
		if tid, err := uuid.Parse(tenant); err == nil {
			if tc, ok, _ := s.cfg.Teams.TeamConfig(ctx, tid, t.Team); ok {
				rc.Strategy = tc.Strategy
				rc.RingSeconds = time.Duration(tc.RingSeconds) * time.Second
				rc.MaxWait = time.Duration(tc.MaxWait) * time.Second
			}
		}
	}

	eff := &pionRing{s: s, tenant: tenant, roomName: name, label: t.Label, ring: ring, rung: map[string]bool{}}
	if outcome, _, _ := s.router.Run(ctx, rc, candidates, eff); outcome == routing.OutcomeCompleted {
		return "completed", nil
	}
	return "no_answer", nil
}

// pionRing is the pion-plane routing.Effects adapter: it rings agents by pushing
// msgIncoming over their presence sockets and learns of an answer when an agent
// joins the room (signalRing closes `ring`).
type pionRing struct {
	s        *Server
	tenant   string
	roomName string          // un-namespaced name agents join
	label    string          // caller label shown to the agent
	ring     <-chan struct{} // closed on a room join
	rung     map[string]bool // sips already popped this transfer
}

// Answered backstops the async case: an agent joined mid-hold (outside a Ring).
func (p *pionRing) Answered() (bool, string) {
	select {
	case <-p.ring:
		return true, ""
	default:
		return false, ""
	}
}

// CallerAlive: the pion plane relies on the IVR driver's ctx, cancelled when the
// call ends; the caller is a room participant, not polled here.
func (p *pionRing) CallerAlive(ctx context.Context) bool { return ctx.Err() == nil }

// Ring pops the incoming-call UI on the ordered agents (deduped within a
// transfer) and waits up to perAttempt for one to join the room.
func (p *pionRing) Ring(ctx context.Context, ordered []string, perAttempt time.Duration) (routing.RingOutcome, error) {
	p.s.mu.Lock()
	inSet := make(map[string]bool, len(ordered))
	for _, sip := range ordered {
		inSet[sip] = true
	}
	for sip := range p.rung { // forget agents no longer available so they re-pop if freed
		if !inSet[sip] {
			delete(p.rung, sip)
		}
	}
	for _, sip := range ordered {
		if !p.rung[sip] {
			if c := p.s.agents[p.tenant][sip]; c != nil {
				_ = c.send(msg{Type: msgIncoming, Room: p.roomName, From: p.label})
				p.rung[sip] = true
			}
		}
	}
	p.s.mu.Unlock()

	select {
	case <-p.ring:
		return routing.RingOutcome{Answered: true}, nil
	case <-time.After(perAttempt):
		return routing.RingOutcome{}, nil
	case <-ctx.Done():
		return routing.RingOutcome{}, ctx.Err()
	}
}

// Hold waits up to maxDur, returning early when ready() goes true or an agent
// joins. No hold audio on the web plane yet — the caller stays connected.
func (p *pionRing) Hold(ctx context.Context, maxDur time.Duration, ready func() bool) {
	tk := time.NewTicker(250 * time.Millisecond)
	defer tk.Stop()
	timeout := time.After(maxDur)
	for {
		select {
		case <-p.ring:
			return
		case <-ctx.Done():
			return
		case <-timeout:
			return
		case <-tk.C:
			if ready() {
				return
			}
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

// ensure the server satisfies the IVR's agent-ring seam.
var _ ivrdrive.Agents = (*Server)(nil)
