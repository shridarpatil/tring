package signaling

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/shridarpatil/tring/internal/brain"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/routing"
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
	if n > 0 {
		s.publishPresence(tenant, sip, presence.StatusOnCall)
	} else {
		s.publishPresence(tenant, sip, presence.StatusOnline)
	}
}

// publishPresence emits an agent.presence event for the dashboard live feed.
// Called only on actual transitions (connect / on_call flip / disconnect), never
// on the TTL refresh tick. Frontend matches agents by sip_username.
func (s *Server) publishPresence(tenant, sip, status string) {
	if s.cfg.Events == nil || sip == "" {
		return
	}
	s.cfg.Events.Publish(context.Background(), tenant, events.TypeAgentPresence, map[string]any{
		"sip_username": sip,
		"status":       status,
	})
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
	// Publish to the shared Redis presence store the API + admin console read, and
	// refresh its TTL while the socket is up (mirrors the legacy /agent/ws).
	if pres := s.cfg.Presence; pres != nil {
		pres.MarkOnline(context.Background(), tenant, sip)
		s.publishPresence(tenant, sip, presence.StatusOnline)
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
			s.publishPresence(tenant, sip, presence.StatusOffline)
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
	}()

	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		if _, err := c.read(); err != nil {
			return
		}
		// Presence socket only takes keepalives; calls happen on a join socket.
	}
}

// Prepare satisfies brain.RingTransport: it wires the pion ring transport for
// one transfer — candidates from the live socket registry + the pionRing Effects
// + a cleanup that withdraws popups and deregisters the pending-ring channel.
// team resolution + the routing.Router queue/ring loop live in brain.AgentRouter.
func (s *Server) Prepare(ctx context.Context, roomID string, t ivrdrive.Target, ringAll bool, sips []string, strategy, moh string) (func(context.Context) []string, routing.Effects, func()) {
	tenant, name, _ := strings.Cut(roomID, "/")

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

	rid := room.RoomID(roomID)
	ring := make(chan struct{})
	s.mu.Lock()
	s.rings[rid] = ring
	s.mu.Unlock()

	eff := &pionRing{s: s, tenant: tenant, roomID: rid, roomName: name, label: t.Label, strategy: strategy, moh: moh, ring: ring, rung: map[string]bool{}}
	cleanup := func() {
		eff.stopHold() // stop the queue/ring hold music (no-op if it never started)
		eff.cancelRung()
		s.mu.Lock()
		if s.rings[rid] == ring {
			delete(s.rings, rid)
		}
		s.mu.Unlock()
	}
	return candidates, eff, cleanup
}

// pionRing is the pion-plane routing.Effects adapter: it rings agents by pushing
// msgIncoming over their presence sockets and learns of an answer when an agent
// joins the room (signalRing closes `ring`).
type pionRing struct {
	s        *Server
	tenant   string
	roomID   room.RoomID     // full "<tenant>/<name>" — where hold music is injected
	roomName string          // un-namespaced name agents join
	label    string          // caller label shown to the agent
	strategy string          // team strategy; "" / ring_all → all at once, else one at a time
	moh      string          // resolved team hold music played to the caller while waiting
	ring     <-chan struct{} // closed on a room join
	rung     map[string]bool // sips popped (and not yet withdrawn) this transfer

	holdOnce sync.Once // starts hold music at most once per transfer
	holdStop func()    // stops it; set by ensureHold, called by stopHold/cleanup
}

// ensureHold starts the caller's hold music the first time the caller is rung or
// held, and keeps it playing for the rest of the transfer (across ring/hold
// iterations). Idempotent — safe to call every Ring/Hold pass.
func (p *pionRing) ensureHold() {
	p.holdOnce.Do(func() { p.holdStop = p.s.playHold(p.roomID, p.moh) })
}

// stopHold ends the caller's hold music (once an agent answers, the wait times
// out, or the transfer is torn down). Safe if hold never started.
func (p *pionRing) stopHold() {
	if p.holdStop != nil {
		p.holdStop()
	}
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

// Ring pops the incoming-call UI per the team strategy and waits up to perAttempt
// for an agent to join the room. ring_all (or no strategy) pops ALL available
// agents at once; the ordered strategies (round_robin / sequential / longest_wait)
// pop exactly ONE — the next in OrderByStrategy order not yet tried this round —
// mirroring the FreeSWITCH plane's '|' one-at-a-time bridge. On no-answer the
// single rung agent's popup is withdrawn so only one agent ever rings at a time;
// the Router's next iteration re-orders and Ring advances to the next.
func (p *pionRing) Ring(ctx context.Context, ordered []string, perAttempt time.Duration) (routing.RingOutcome, error) {
	p.ensureHold() // caller hears hold music from the first ring, not just once queued
	oneAtATime := p.strategy != "" && p.strategy != "ring_all"

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

	pop := func(sip string) {
		if c := p.s.agents[p.tenant][sip]; c != nil {
			_ = c.send(msg{Type: msgIncoming, Room: p.roomName, From: p.label})
			p.rung[sip] = true
		}
	}

	var rungNow string // the single agent rung this attempt (one-at-a-time only)
	if oneAtATime {
		next := ""
		for _, sip := range ordered {
			if !p.rung[sip] {
				next = sip
				break
			}
		}
		if next == "" && len(ordered) > 0 { // all tried, none answered → new round
			p.rung = map[string]bool{}
			next = ordered[0]
		}
		if next != "" {
			pop(next)
			rungNow = next
		}
	} else {
		for _, sip := range ordered {
			if !p.rung[sip] {
				pop(sip)
			}
		}
	}
	p.s.mu.Unlock()

	select {
	case <-p.ring:
		return routing.RingOutcome{Answered: true}, nil
	case <-time.After(perAttempt):
		// One-at-a-time: withdraw this agent's popup before the Router advances, so
		// only ever one agent is ringing. (ring_all keeps everyone popped.)
		if oneAtATime && rungNow != "" {
			p.cancelSips([]string{rungNow})
		}
		return routing.RingOutcome{}, nil
	case <-ctx.Done():
		return routing.RingOutcome{}, ctx.Err()
	}
}

// cancelSips withdraws the incoming-call UI on the given agents (best-effort).
func (p *pionRing) cancelSips(sips []string) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	for _, sip := range sips {
		if c := p.s.agents[p.tenant][sip]; c != nil {
			_ = c.send(msg{Type: msgCancelRing, Room: p.roomName})
		}
	}
}

// cancelRung withdraws the popup on every agent rung this transfer — used as the
// catch-all teardown (answered elsewhere, timed out, or caller hung up) so no
// stale "incoming call" UI is left ringing on the losers.
func (p *pionRing) cancelRung() {
	p.s.mu.Lock()
	sips := make([]string, 0, len(p.rung))
	for sip := range p.rung {
		sips = append(sips, sip)
	}
	p.rung = map[string]bool{}
	p.s.mu.Unlock()
	p.cancelSips(sips)
}

// Hold waits up to maxDur, returning early when ready() goes true or an agent
// joins. The caller hears the team's hold music (started here / on the first
// Ring, stopped when the transfer tears down) for the whole wait.
func (p *pionRing) Hold(ctx context.Context, maxDur time.Duration, ready func() bool) {
	p.ensureHold()
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

// ensure the server satisfies the brain's ring-transport + handoff-wait seams.
var _ brain.RingTransport = (*Server)(nil)
var _ brain.HandoffWaiter = (*Server)(nil)
