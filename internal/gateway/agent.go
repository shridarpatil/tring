package gateway

// Agent signaling (ADR-001 M4b). Agents no longer speak SIP. Instead an agent's
// browser holds one authenticated WebSocket to the gateway — its presence and
// its incoming-call channel. When FreeSWITCH delivers an agent call leg to the
// gateway (as a SIP endpoint; see fsuas.go), the gateway finds the agent's live
// WebSocket, rings the browser, and bridges the browser's WebRTC to the FS RTP
// leg. No agent SIP credentials ever live at the gateway.
//
// This file is the browser-facing half: presence registry + WS message
// protocol + per-call signaling handles. The SIP UAS that drives these handles
// is fsuas.go.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/presence"
)

// agentKey identifies an agent uniquely across tenants. sip is the agent's SIP
// username (agents.sip_username); the FS call leg carries both so the gateway
// can resolve the right browser.
type agentKey struct {
	tenant string
	sip    string
}

// agentMsg is the JSON envelope exchanged over the agent WebSocket. Not every
// field is used by every type — see the type constants.
type agentMsg struct {
	Type   string `json:"type"`
	CallID string `json:"call_id,omitempty"`
	SDP    string `json:"sdp,omitempty"`
	From   string `json:"from,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Auto, set on a s→c "incoming", tells the browser to auto-answer this leg
	// (a backend click-to-call the agent already initiated) rather than ring
	// Accept/Decline. True inbound / IVR transfers omit it.
	Auto bool `json:"auto,omitempty"`
}

// Message types. Server→client: ready, incoming, answer, ended. Client→server:
// offer (accept + browser SDP), reject, hangup, ping (keepalive).
const (
	msgReady    = "ready"    // s→c: WS up, presence registered
	msgIncoming = "incoming" // s→c: a call is ringing for this agent
	msgAnswer   = "answer"   // s→c: gateway's WebRTC answer (call established)
	msgEnded    = "ended"    // s→c: the call ended (remote hangup / failure)
	msgOffer    = "offer"    // c→s: accept the incoming call with this WebRTC offer
	msgReject   = "reject"   // c→s: decline a ringing call
	msgHangup   = "hangup"   // c→s: end an in-progress call
	msgPing     = "ping"     // c→s: keepalive (resets the read deadline; no reply)
)

// agentRegistry tracks online agents keyed by (tenant, sip). An agent may hold
// MULTIPLE sessions at once (several tabs/devices) — they all coexist; a call
// rings every session and whoever answers first wins (see handleFSInvite). The
// registry also OWNS the agent's Redis presence: one refresh ticker per agent
// (not per session) publishes the aggregate state (on_call if ANY session is on
// a call, else online), so the per-session tickers can't clobber each other.
type agentRegistry struct {
	mu       sync.Mutex
	m        map[agentKey]*agentGroup
	presence *presence.Store
}

// agentGroup is all of one agent's live sessions plus their aggregate call
// count and the stop channel for that agent's presence refresh ticker.
type agentGroup struct {
	sessions map[*agentSession]struct{}
	active   int           // bridged calls in progress across ALL sessions
	stop     chan struct{} // closed when the last session leaves → stops the ticker
}

func newAgentRegistry(p *presence.Store) *agentRegistry {
	return &agentRegistry{m: make(map[agentKey]*agentGroup), presence: p}
}

// add registers a session. If it's the agent's first, presence goes online and
// a refresh ticker starts; otherwise it just joins the existing group.
func (r *agentRegistry) add(s *agentSession) {
	r.mu.Lock()
	g := r.m[s.key]
	first := g == nil
	if first {
		g = &agentGroup{sessions: map[*agentSession]struct{}{}, stop: make(chan struct{})}
		r.m[s.key] = g
	}
	g.sessions[s] = struct{}{}
	stop := g.stop
	r.mu.Unlock()
	r.publish(s.key)
	if first {
		go r.refreshLoop(s.key, stop)
	}
}

// remove deregisters a session. When the agent's LAST session leaves, the
// refresh ticker stops and presence goes offline; otherwise presence is just
// re-published (the agent is still online via the remaining sessions).
func (r *agentRegistry) remove(s *agentSession) {
	r.mu.Lock()
	g := r.m[s.key]
	if g == nil {
		r.mu.Unlock()
		return
	}
	delete(g.sessions, s)
	last := len(g.sessions) == 0
	if last {
		close(g.stop)
		delete(r.m, s.key)
	}
	r.mu.Unlock()
	if last {
		r.presence.Offline(context.Background(), s.key.tenant, s.key.sip)
	} else {
		r.publish(s.key)
	}
}

// onCall reports whether the agent has any bridged call in progress (across all
// tabs). The orchestrator's ring-all already skips on_call agents via presence,
// but a fresh INVITE can still arrive in the race window between enterCall and
// the next routing decision — fsuas consults this to reply 486 rather than pop a
// second incoming-call UI on a busy agent.
func (r *agentRegistry) onCall(k agentKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.m[k]
	return g != nil && g.active > 0
}

// sessionsFor returns a snapshot of the agent's live sessions (nil if offline).
func (r *agentRegistry) sessionsFor(k agentKey) []*agentSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.m[k]
	if g == nil {
		return nil
	}
	out := make([]*agentSession, 0, len(g.sessions))
	for s := range g.sessions {
		out = append(out, s)
	}
	return out
}

// enterCall / leaveCall adjust the agent's aggregate on-call count and re-publish
// presence. The orchestrator's ring-all skips an agent whose aggregate is >0, so
// a second call isn't routed to an agent already on one (on any tab).
func (r *agentRegistry) enterCall(k agentKey) {
	r.mu.Lock()
	if g := r.m[k]; g != nil {
		g.active++
	}
	r.mu.Unlock()
	r.publish(k)
}

func (r *agentRegistry) leaveCall(k agentKey) {
	r.mu.Lock()
	nowFree := false
	if g := r.m[k]; g != nil && g.active > 0 {
		g.active--
		nowFree = g.active == 0
	}
	r.mu.Unlock()
	// Stamp the moment this agent became free so the longest_wait ring strategy
	// can order idle agents. Only on the transition to zero active calls.
	if nowFree {
		r.presence.MarkFreed(context.Background(), k.tenant, k.sip)
	}
	r.publish(k)
}

// publish writes the agent's current aggregate presence (on_call if any session
// is on a call, else online), refreshing the TTL. No-op if the agent is gone.
func (r *agentRegistry) publish(k agentKey) {
	r.mu.Lock()
	g := r.m[k]
	if g == nil {
		r.mu.Unlock()
		return
	}
	onCall := g.active > 0
	r.mu.Unlock()
	if onCall {
		r.presence.MarkOnCall(context.Background(), k.tenant, k.sip)
	} else {
		r.presence.MarkOnline(context.Background(), k.tenant, k.sip)
	}
}

// refreshLoop keeps the agent's presence TTL alive until the group is gone.
func (r *agentRegistry) refreshLoop(k agentKey, stop chan struct{}) {
	t := time.NewTicker(presence.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.publish(k)
		}
	}
}

// offerFrom is a browser's accept (WebRTC offer SDP) tagged with the session it
// came from, so a call rung across several sessions knows WHICH one to bridge.
type offerFrom struct {
	sess *agentSession
	sdp  string
}

// incomingCall coordinates one inbound call's signaling across ALL of an agent's
// sessions (tabs). The SIP UAS rings every session via Gateway.ringAgent, then
// waits on offer/closed: the first session to accept wins (offer is buffered 1,
// so later accepts are dropped), the rest are cancelled.
type incomingCall struct {
	id     string
	offer  chan offerFrom // first browser to accept wins
	closed chan struct{}  // declined / all ringing sessions gone
	once   sync.Once

	// teardown tears down the live media bridge once the call is established.
	// nil while still ringing (the SIP UAS is waiting on offer/closed); set by
	// the UAS after the bridge is up so a browser hangup ends the FS leg too.
	mu       sync.Mutex
	teardown func()
	ringers  map[*agentSession]struct{} // sessions still ringing this call
}

func (ic *incomingCall) end() {
	ic.once.Do(func() { close(ic.closed) })
}

// dropRinger removes a session from the ringing set (it disconnected). If the
// call is already established, hanging up that session ends the bridge; else if
// no sessions are left ringing, the call ends (nobody can answer).
func (ic *incomingCall) dropRinger(s *agentSession) {
	ic.mu.Lock()
	if _, ok := ic.ringers[s]; !ok {
		ic.mu.Unlock()
		return // not (or no longer) ringing this call — e.g. a cancelled loser
	}
	delete(ic.ringers, s)
	n := len(ic.ringers)
	fn := ic.teardown
	ic.mu.Unlock()
	if fn != nil {
		fn() // established call, this session hung up → end the bridge
	} else if n == 0 {
		ic.end() // last ringer gone, nobody answered → call lost
	}
}

// setTeardown registers how to tear down the established bridge.
func (ic *incomingCall) setTeardown(fn func()) {
	ic.mu.Lock()
	ic.teardown = fn
	ic.mu.Unlock()
}

// fireTeardown runs the bridge teardown if the call is established; returns
// false if it was still ringing (no teardown set yet).
func (ic *incomingCall) fireTeardown() bool {
	ic.mu.Lock()
	fn := ic.teardown
	ic.mu.Unlock()
	if fn == nil {
		return false
	}
	fn()
	return true
}

// agentSession is one of an agent's live WebSockets (a tab/device). Writes are
// serialized; reads run in handleAgentWS's loop and dispatch client messages to
// this session's pending calls. Presence + on-call counting live on the
// agentGroup (registry), aggregated across all of the agent's sessions.
type agentSession struct {
	key     agentKey
	conn    net.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]*incomingCall
	log     *slog.Logger
}

func (s *agentSession) send(m agentMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return wsutil.WriteServerMessage(s.conn, ws.OpText, b)
}

// ringAgent rings ALL of the agent's live sessions (tabs/devices) for one
// inbound call and returns the shared handle, or nil if the agent has no
// sessions. The first session to accept wins (offer is buffered 1); the caller
// cancels the rest. The caller selects on ic.offer / ic.closed / a timeout.
func (g *Gateway) ringAgent(key agentKey, from string, auto bool) *incomingCall {
	sessions := g.agents.sessionsFor(key)
	if len(sessions) == 0 {
		return nil
	}
	ic := &incomingCall{
		id:      uuid.NewString(),
		offer:   make(chan offerFrom, 1),
		closed:  make(chan struct{}),
		ringers: make(map[*agentSession]struct{}, len(sessions)),
	}
	for _, s := range sessions {
		ic.ringers[s] = struct{}{}
	}
	for _, s := range sessions {
		s.mu.Lock()
		s.pending[ic.id] = ic
		s.mu.Unlock()
		if err := s.send(agentMsg{Type: msgIncoming, CallID: ic.id, From: from, Auto: auto}); err != nil {
			s.finishIncoming(ic.id)
			ic.dropRinger(s) // couldn't notify this tab — drop it from the ring
		}
	}
	return ic
}

// cancelOthers tells every ringing session EXCEPT the winner that the call was
// answered elsewhere and clears it from their pending. The winner keeps it.
func (ic *incomingCall) cancelOthers(winner *agentSession) {
	ic.mu.Lock()
	others := make([]*agentSession, 0, len(ic.ringers))
	for s := range ic.ringers {
		if s != winner {
			others = append(others, s)
		}
	}
	ic.ringers = map[*agentSession]struct{}{winner: {}}
	ic.mu.Unlock()
	for _, s := range others {
		s.notifyEnded(ic.id, "answered elsewhere")
		s.finishIncoming(ic.id)
	}
}

// cancelAll tells every ringing session the call ended (declined / timeout) and
// clears it from their pending.
func (ic *incomingCall) cancelAll(reason string) {
	ic.mu.Lock()
	all := make([]*agentSession, 0, len(ic.ringers))
	for s := range ic.ringers {
		all = append(all, s)
	}
	ic.ringers = map[*agentSession]struct{}{}
	ic.mu.Unlock()
	for _, s := range all {
		s.notifyEnded(ic.id, reason)
		s.finishIncoming(ic.id)
	}
}

// sendAnswer delivers the gateway's WebRTC answer for an accepted call.
func (s *agentSession) sendAnswer(callID, sdp string) error {
	return s.send(agentMsg{Type: msgAnswer, CallID: callID, SDP: sdp})
}

// notifyEnded tells the browser a call ended (remote hangup / bridge failure).
func (s *agentSession) notifyEnded(callID, reason string) {
	_ = s.send(agentMsg{Type: msgEnded, CallID: callID, Reason: reason})
}

func (s *agentSession) finishIncoming(callID string) {
	s.mu.Lock()
	delete(s.pending, callID)
	s.mu.Unlock()
}

// dispatch routes a client message to its pending call.
func (s *agentSession) dispatch(m agentMsg) {
	switch m.Type {
	case msgOffer:
		s.mu.Lock()
		ic := s.pending[m.CallID]
		s.mu.Unlock()
		if ic != nil {
			select {
			case ic.offer <- offerFrom{sess: s, sdp: m.SDP}:
			default: // a sibling tab already won this call
			}
		}
	case msgPing:
		// Keepalive: the read already reset the deadline; nothing else to do.
		return
	case msgReject, msgHangup:
		s.mu.Lock()
		ic := s.pending[m.CallID]
		s.mu.Unlock()
		if ic == nil {
			s.log.Warn("agent msg for unknown call (no pending bridge)",
				"tenant", s.key.tenant, "sip", s.key.sip, "type", m.Type, "call", m.CallID)
			return
		}
		// Established call → tear the bridge down (which BYEs the FS leg).
		// Still ringing → unblock the UAS select so it rejects the INVITE.
		if ic.fireTeardown() {
			s.log.Info("agent ended call, tearing down bridge",
				"tenant", s.key.tenant, "sip", s.key.sip, "type", m.Type, "call", m.CallID)
		} else {
			s.log.Info("agent declined call while ringing",
				"tenant", s.key.tenant, "sip", s.key.sip, "type", m.Type, "call", m.CallID)
			ic.end()
		}
	default:
		s.log.Warn("agent sent unknown msg", "tenant", s.key.tenant, "sip", s.key.sip, "type", m.Type)
	}
}

// close tears down the session: drops it from any call it was ringing (which
// ends a call only if it was the last ringer / had answered it) and closes the
// socket. A sibling tab still ringing the same call is unaffected.
func (s *agentSession) close() {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]*incomingCall{}
	s.mu.Unlock()
	for _, ic := range pending {
		ic.dropRinger(s)
	}
	_ = s.conn.Close()
}

// handleAgentWS upgrades the WebSocket, authenticates the agent token, registers
// presence, and runs the read loop until the socket closes. Browsers can't set
// Authorization on a WS handshake, so the token is accepted from the `token`
// query param (falling back to the bearer header for non-browser clients).
func (g *Gateway) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	raw := bearer(r)
	if raw == "" {
		raw = r.URL.Query().Get("token")
	}
	claims, err := validateToken(raw, g.cfg.JWTSecret)
	if err != nil {
		http.Error(w, `{"error":"invalid or missing token"}`, http.StatusUnauthorized)
		return
	}

	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		g.log.Error("agent ws upgrade failed", "err", err)
		return
	}

	s := &agentSession{
		key:     agentKey{tenant: claims.TenantID.String(), sip: claims.SipUsername},
		conn:    conn,
		pending: map[string]*incomingCall{},
		log:     g.log,
	}
	// add registers the session and owns presence (online + refresh ticker)
	// aggregated across all of this agent's sessions/tabs.
	g.agents.add(s)
	g.log.Info("agent online", "tenant", s.key.tenant, "sip", s.key.sip)
	_ = s.send(agentMsg{Type: msgReady})

	defer func() {
		// remove deregisters this session; presence goes offline only when the
		// agent's LAST session leaves (the registry handles that).
		g.agents.remove(s)
		s.close()
		g.log.Info("agent offline", "tenant", s.key.tenant, "sip", s.key.sip)
	}()

	for {
		// A generous read deadline doubles as a dead-peer reaper; the SDK pings
		// (or any client message) keeps it alive.
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		data, op, err := wsutil.ReadClientData(conn)
		if err != nil {
			return
		}
		if op == ws.OpClose {
			return
		}
		if op != ws.OpText {
			continue
		}
		var m agentMsg
		if err := json.Unmarshal(data, &m); err != nil {
			g.log.Warn("agent bad msg", "tenant", s.key.tenant, "sip", s.key.sip, "err", err)
			continue
		}
		s.dispatch(m)
	}
}
