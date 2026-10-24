package signaling

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

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
	// Away until sendAvailability has read the backend flag, so a ring in that
	// gap can't reach an agent the backend set Away.
	if s.away == nil {
		s.away = map[string]bool{}
	}
	s.away[tenant+"|"+sip] = true
	s.mu.Unlock()
	_ = c.send(msg{Type: msgReady})
	s.log.Info("agent online", "tenant", tenant, "sip", sip)
	// Read after registering above, so a change that lands meanwhile is either
	// in this read or pushed to the socket by PushAvailability.
	s.sendAvailability(c, tenant, sip)
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
			delete(s.away, tenant+"|"+sip)
		}
		s.mu.Unlock()
		_ = c.Close()
		s.log.Info("agent offline", "tenant", tenant, "sip", sip)
	}()

	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		m, err := c.read()
		if err != nil {
			return
		}
		// The presence socket carries keepalives and one call-control message: a
		// decline, which advances the router to the next agent immediately (the
		// answer happens on a separate join socket).
		if m.Type == msgDecline {
			s.handleDecline(tenant, sip, m.Room)
		}
	}
}

// sendAvailability tells a just-connected agent their backend Away/Break flag.
// Only Away is worth a message (the SDK assumes available), and a failed lookup
// leaves the agent showing Online rather than blocking the connect.
//
// The agent was registered as Away (handleAgentPresence), so no ring reaches
// them before this lookup lands. A live push that arrives while it's in flight
// is newer than what the lookup read, so the lookup then yields to it.
func (s *Server) sendAvailability(c *conn, tenant, sip string) {
	available, ok := s.lookupAvailability(tenant, sip)
	if !ok {
		available = true // unknown: ringable, as before the Away flag reached the gateway
	}
	c.availMu.Lock()
	defer c.availMu.Unlock()
	if c.availPushed || !s.setAway(c, tenant, sip, !available) {
		return
	}
	if !available {
		_ = c.send(msg{Type: msgAvailability, Available: &available})
	}
}

// lookupAvailability reads an agent's available_for_calls through the Redis
// cache (the API rewrites it on update), so reconnects don't hit the DB.
// ok=false when it can't be known.
func (s *Server) lookupAvailability(tenant, sip string) (available, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if available, ok := s.cfg.Presence.CachedAvailability(ctx, tenant, sip); ok {
		return available, true
	}
	if s.cfg.Availability == nil {
		return false, false
	}
	tid, err := uuid.Parse(tenant)
	if err != nil {
		return false, false
	}
	available, err = s.cfg.Availability.AgentAvailable(ctx, tid, sip)
	if err != nil {
		s.log.Warn("agent availability lookup failed", "tenant", tenant, "sip", sip, "err", err)
		return false, false
	}
	s.cfg.Presence.FillAvailability(ctx, tenant, sip, available)
	return available, true
}

// PushAvailability forwards a backend availability change (the API's
// agent.status event, POST /v1/agents/{id}/availability) to the agent's
// presence socket on this gateway, if they're connected here.
func (s *Server) PushAvailability(tenant, sip string, available bool) {
	s.mu.Lock()
	c := s.agents[tenant][sip]
	s.mu.Unlock()
	if c == nil {
		return
	}
	c.availMu.Lock()
	defer c.availMu.Unlock()
	c.availPushed = true
	if s.setAway(c, tenant, sip, !available) {
		_ = c.send(msg{Type: msgAvailability, Available: &available})
	}
}

// setAway records whether a connected agent is backend-marked Away, so the ring
// candidates skip them (the team lookup already filters on available_for_calls;
// ring-all and direct-agent targets come from the live socket registry instead).
// It reports false, changing nothing, when c is no longer the agent's socket.
func (s *Server) setAway(c *conn, tenant, sip string, away bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agents[tenant][sip] != c {
		return false
	}
	k := tenant + "|" + sip
	if !away {
		delete(s.away, k)
		return true
	}
	if s.away == nil {
		s.away = map[string]bool{}
	}
	s.away[k] = true
	return true
}

// handleDecline delivers an agent's rejection of a ringing call to that room's
// active ring (registered by Prepare), which advances the router to the next
// agent without waiting out the ring timeout. No-op if the ring already ended.
func (s *Server) handleDecline(tenant, sip, roomName string) {
	if roomName == "" {
		return
	}
	rid := room.RoomID(tenant + "/" + roomName)
	s.mu.Lock()
	dec := s.declines[rid]
	s.mu.Unlock()
	if dec != nil {
		dec(sip)
	}
}

// Prepare satisfies brain.RingTransport: it wires the pion ring transport for
// one transfer — candidates from the live socket registry + the pionRing Effects
// + a cleanup that withdraws popups and deregisters the pending-ring channel.
// team resolution + the routing.Router queue/ring loop live in brain.AgentRouter.
func (s *Server) Prepare(ctx context.Context, roomID string, t ivrdrive.Target, ringAll bool, sips []string, strategy, moh string) (func(context.Context) []string, routing.Effects, func()) {
	tenant, name, _ := strings.Cut(roomID, "/")
	rid := room.RoomID(roomID)
	ring := make(chan struct{})
	eff := &pionRing{
		s: s, tenant: tenant, roomID: rid, roomName: name, label: t.Label,
		strategy: strategy, moh: moh, ring: ring,
		rung: map[string]bool{}, active: map[string]bool{}, declined: map[string]bool{},
		declineCh: make(chan struct{}, 1),
	}

	// candidates excludes agents who declined this call (as well as busy, Away
	// and offline ones) so the router never re-rings a decliner and the
	// queue/hold logic sees the true remaining pool.
	candidates := func(context.Context) []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		free := func(sip string) bool {
			k := tenant + "|" + sip
			return s.onCall[k] == 0 && !s.away[k] && !eff.declined[sip]
		}
		var out []string
		if ringAll {
			for sip := range s.agents[tenant] {
				if free(sip) {
					out = append(out, sip)
				}
			}
		} else {
			for _, sip := range sips {
				if s.agents[tenant][sip] != nil && free(sip) {
					out = append(out, sip)
				}
			}
		}
		return out
	}

	s.mu.Lock()
	s.rings[rid] = ring
	s.declines[rid] = eff.decline
	s.mu.Unlock()

	// Surface "ringing → <target>" on the live dashboard for the whole routing
	// window (queue-hold + ring). Team/agent naming is resolved by callcdr.
	if s.cfg.Calls != nil {
		s.cfg.Calls.RingStart(ctx, roomID, t.Team, t.AgentSIP)
	}

	cleanup := func() {
		// Stop the queue/ring hold music (no-op if it never started). An agent who
		// answered has joined, but their audio only flows once their WebRTC
		// connection is up — keep the music until then so the caller isn't left in
		// silence for that gap.
		select {
		case <-ring:
			go func() {
				s.waitMediaUp(rid, answerMediaWait)
				eff.stopHold()
			}()
		default:
			eff.stopHold()
		}
		eff.cancelRung()
		if s.cfg.Calls != nil {
			s.cfg.Calls.RingStop(roomID) // no-op if answered — reverts to the IVR phase on no-answer
		}
		s.mu.Lock()
		if s.rings[rid] == ring {
			delete(s.rings, rid)
		}
		delete(s.declines, rid)
		delete(s.invites, rid) // ring over: a late answer can no longer join
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
	rung     map[string]bool // sips popped at least once this transfer (suppresses re-pop within a round)
	active   map[string]bool // sips whose popup is currently showing (guarded by s.mu)
	declined map[string]bool // sips that rejected this call — excluded from candidates (guarded by s.mu)

	declineCh chan struct{} // buffered(1) notify: an agent declined; Ring re-checks `active`

	holdOnce sync.Once // starts hold music at most once per transfer
	holdStop func()    // stops it; set by ensureHold, called by stopHold/cleanup
}

// ensureHold starts the caller's hold music the first time the caller is rung or
// held, and keeps it playing for the rest of the transfer (across ring/hold
// iterations). Idempotent — safe to call every Ring/Hold pass.
func (p *pionRing) ensureHold() {
	p.holdOnce.Do(func() { p.holdStop = p.s.playHold(p.roomID, p.effectiveMOH()) })
}

// effectiveMOH resolves what the waiting caller should hear. The team MOH wins
// when it's a playable file; otherwise (no team config — e.g. a direct-agent
// transfer where moh is always "" — or a stream sentinel pion can't play) it
// falls back to the tenant's system hold music so the caller hears ringback, not
// silence. Mirrors the agent-initiated hold path (holdParty → tenantHoldMusic).
func (p *pionRing) effectiveMOH() string {
	if holdFile(p.moh) != "" {
		return p.moh
	}
	return p.s.tenantHoldMusic(p.roomID)
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

	var poppedNow []string // agents actually rung this call — recorded on the timeline below
	pop := func(sip string) {
		if c := p.s.agents[p.tenant][sip]; c != nil {
			_ = c.send(msg{Type: msgIncoming, Room: p.roomName, From: p.label})
			if p.s.invites[p.roomID] == nil {
				p.s.invites[p.roomID] = map[string]bool{}
			}
			p.s.invites[p.roomID][sip] = true
			p.rung[sip] = true
			p.active[sip] = true
			poppedNow = append(poppedNow, sip)
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

	// Record each agent actually rung on the durable timeline (id + name resolved
	// by callcdr), off the lock. ring_all pops several at once; a one-at-a-time
	// strategy pops one per attempt, so these land in the order agents were tried.
	if p.s.cfg.Calls != nil {
		for _, sip := range poppedNow {
			p.s.cfg.Calls.RingAgent(string(p.roomID), sip)
		}
	}

	timer := time.NewTimer(perAttempt)
	defer timer.Stop()
	for {
		select {
		case <-p.ring:
			return routing.RingOutcome{Answered: true}, nil
		case <-timer.C:
			// One-at-a-time: withdraw this agent's popup before the Router advances, so
			// only ever one agent is ringing. (ring_all keeps everyone popped.)
			if oneAtATime && rungNow != "" {
				p.cancelSips([]string{rungNow})
			}
			return routing.RingOutcome{}, nil
		case <-p.declineCh:
			// An agent rejected. decline() already withdrew that popup and dropped it
			// from `active` (+ marked it declined so it won't be re-rung). If nobody
			// we rang is still ringing, advance to the next agent NOW; otherwise
			// (ring_all with others still ringing) keep waiting out the window.
			p.s.mu.Lock()
			stillRinging := len(p.active)
			p.s.mu.Unlock()
			if stillRinging == 0 {
				return routing.RingOutcome{}, nil
			}
		case <-ctx.Done():
			return routing.RingOutcome{}, ctx.Err()
		}
	}
}

// cancelSips withdraws the incoming-call UI on the given agents (best-effort) and
// clears their active-popup flag.
func (p *pionRing) cancelSips(sips []string) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	for _, sip := range sips {
		delete(p.active, sip)
		if c := p.s.agents[p.tenant][sip]; c != nil {
			_ = c.send(msg{Type: msgCancelRing, Room: p.roomName})
		}
	}
}

// decline records an agent's rejection of the current ring: it withdraws that
// agent's popup, marks the agent as declined for this call (so candidates drops
// it and the router won't re-ring it), and wakes Ring so it advances to the next
// agent immediately instead of waiting out the ring window. Registered per active
// ring as s.declines[roomID] by Prepare.
func (p *pionRing) decline(sip string) {
	p.s.mu.Lock()
	p.declined[sip] = true
	wasActive := p.active[sip]
	delete(p.active, sip)
	p.s.mu.Unlock()
	if wasActive {
		p.cancelSips([]string{sip}) // withdraw the ringing popup on the decliner
	}
	// Nudge Ring to re-evaluate (buffered(1) → coalesces concurrent declines).
	select {
	case p.declineCh <- struct{}{}:
	default:
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

// roomOccupied reports whether roomID is a live call: it has participants or a
// phone/outbound leg attached.
func (s *Server) roomOccupied(roomID room.RoomID) bool {
	if r, ok := s.rooms.Get(roomID); ok && len(r.Participants()) > 0 {
		return true
	}
	return s.sfu.HasPort(roomID)
}

// takeInvite consumes the room's ring invitations if sip is one of the agents
// rung for it. The first answer takes them all, so with ring_all only one agent
// gets in.
func (s *Server) takeInvite(roomID room.RoomID, sip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.invites[roomID][sip] {
		return false
	}
	delete(s.invites, roomID)
	return true
}

// ensure the server satisfies the brain's ring-transport + handoff-wait seams.
var _ brain.RingTransport = (*Server)(nil)
var _ brain.HandoffWaiter = (*Server)(nil)
