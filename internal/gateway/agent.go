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
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
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
// offer (accept + browser SDP), reject, hangup.
const (
	msgReady    = "ready"    // s→c: WS up, presence registered
	msgIncoming = "incoming" // s→c: a call is ringing for this agent
	msgAnswer   = "answer"   // s→c: gateway's WebRTC answer (call established)
	msgEnded    = "ended"    // s→c: the call ended (remote hangup / failure)
	msgOffer    = "offer"    // c→s: accept the incoming call with this WebRTC offer
	msgReject   = "reject"   // c→s: decline a ringing call
	msgHangup   = "hangup"   // c→s: end an in-progress call
)

// agentRegistry tracks online agents keyed by (tenant, sip). One session per
// agent — a new connection for the same key evicts the old one (last-writer
// wins, the common "agent reloaded the tab" case).
type agentRegistry struct {
	mu sync.RWMutex
	m  map[agentKey]*agentSession
}

func newAgentRegistry() *agentRegistry {
	return &agentRegistry{m: make(map[agentKey]*agentSession)}
}

func (r *agentRegistry) add(s *agentSession) {
	r.mu.Lock()
	old := r.m[s.key]
	r.m[s.key] = s
	r.mu.Unlock()
	if old != nil && old != s {
		old.close() // evict the stale session
	}
}

// remove deletes s only if it's still the registered session for its key (a
// later reconnect must not be clobbered by the old session's teardown).
func (r *agentRegistry) remove(s *agentSession) {
	r.mu.Lock()
	if r.m[s.key] == s {
		delete(r.m, s.key)
	}
	r.mu.Unlock()
}

func (r *agentRegistry) lookup(k agentKey) (*agentSession, bool) {
	r.mu.RLock()
	s, ok := r.m[k]
	r.mu.RUnlock()
	return s, ok
}

// incomingCall coordinates one inbound call's signaling with the browser. The
// SIP UAS creates it via agentSession.startIncoming, then waits on offer/closed.
type incomingCall struct {
	id     string
	offer  chan string   // browser's WebRTC offer SDP (it accepted)
	closed chan struct{} // browser rejected / hung up / disconnected
	once   sync.Once

	// teardown tears down the live media bridge once the call is established.
	// nil while still ringing (the SIP UAS is waiting on offer/closed); set by
	// the UAS after the bridge is up so a browser hangup ends the FS leg too.
	mu       sync.Mutex
	teardown func()
}

func (ic *incomingCall) end() {
	ic.once.Do(func() { close(ic.closed) })
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

// agentSession is one online agent's WebSocket. Writes are serialized; reads run
// in handleAgentWS's loop and dispatch client messages to pending calls.
type agentSession struct {
	key      agentKey
	conn     net.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]*incomingCall
	presence *presence.Store // publishes online / on_call to Redis
	active   atomic.Int32    // bridged calls in progress (>0 ⇒ "on_call")
	log      *log.Logger
}

// publishPresence asserts the agent's CURRENT state and refreshes its TTL: busy
// on a call (on_call) wins, else available (online). Call enter/leave and the
// periodic refresh all go through here so the TTL refresh never clobbers a busy
// marker back to online.
func (s *agentSession) publishPresence(ctx context.Context) {
	if s.active.Load() > 0 {
		s.presence.MarkOnCall(ctx, s.key.tenant, s.key.sip)
	} else {
		s.presence.MarkOnline(ctx, s.key.tenant, s.key.sip)
	}
}

// enterCall marks the agent busy ("on_call") for a bridged call. While busy the
// orchestrator's ring-all skips this agent, so no second call is routed to one
// already on a call.
func (s *agentSession) enterCall(ctx context.Context) {
	s.active.Add(1)
	s.publishPresence(ctx)
}

// leaveCall is the counterpart: when the last call ends the agent is available again.
func (s *agentSession) leaveCall(ctx context.Context) {
	s.active.Add(-1)
	s.publishPresence(ctx)
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

// startIncoming registers a ringing call and notifies the browser. The returned
// handle's offer channel fires when the agent accepts; closed fires on
// reject/hangup/disconnect. Caller must call finishIncoming when done.
func (s *agentSession) startIncoming(from string, auto bool) *incomingCall {
	ic := &incomingCall{
		id:     uuid.NewString(),
		offer:  make(chan string, 1),
		closed: make(chan struct{}),
	}
	s.mu.Lock()
	s.pending[ic.id] = ic
	s.mu.Unlock()
	if err := s.send(agentMsg{Type: msgIncoming, CallID: ic.id, From: from, Auto: auto}); err != nil {
		s.finishIncoming(ic.id)
		ic.end()
	}
	return ic
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
			case ic.offer <- m.SDP:
			default:
			}
		}
	case msgReject, msgHangup:
		s.mu.Lock()
		ic := s.pending[m.CallID]
		s.mu.Unlock()
		if ic == nil {
			s.log.Printf("gateway: agent %s/%s %s for unknown call %q (no pending bridge)",
				s.key.tenant, s.key.sip, m.Type, m.CallID)
			return
		}
		// Established call → tear the bridge down (which BYEs the FS leg).
		// Still ringing → unblock the UAS select so it rejects the INVITE.
		if ic.fireTeardown() {
			s.log.Printf("gateway: agent %s/%s %s — tearing down bridge for call %s",
				s.key.tenant, s.key.sip, m.Type, m.CallID)
		} else {
			s.log.Printf("gateway: agent %s/%s %s while ringing — declining call %s",
				s.key.tenant, s.key.sip, m.Type, m.CallID)
			ic.end()
		}
	default:
		s.log.Printf("gateway: agent %s/%s sent unknown msg %q", s.key.tenant, s.key.sip, m.Type)
	}
}

// close tears down the session: ends all pending calls and closes the socket.
func (s *agentSession) close() {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]*incomingCall{}
	s.mu.Unlock()
	for _, ic := range pending {
		ic.end()
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
		g.log.Printf("gateway: agent ws upgrade failed: %v", err)
		return
	}

	s := &agentSession{
		key:      agentKey{tenant: claims.TenantID.String(), sip: claims.SipUsername},
		conn:     conn,
		pending:  map[string]*incomingCall{},
		presence: g.presence,
		log:      g.log,
	}
	g.agents.add(s)
	g.log.Printf("gateway: agent online %s/%s", s.key.tenant, s.key.sip)
	_ = s.send(agentMsg{Type: msgReady})

	// Publish presence to Redis (read by the API badge + orchestrator transfer)
	// and keep it fresh while the socket is up; it ages out within TTL of a
	// disconnect/crash. No-op when Redis is disabled.
	bg := context.Background()
	g.presence.MarkOnline(bg, s.key.tenant, s.key.sip)
	stopRefresh := make(chan struct{})
	go func() {
		t := time.NewTicker(presence.RefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-stopRefresh:
				return
			case <-t.C:
				s.publishPresence(bg) // preserves on_call / away / online
			}
		}
	}()

	defer func() {
		close(stopRefresh)
		g.presence.Offline(bg, s.key.tenant, s.key.sip)
		g.agents.remove(s)
		s.close()
		g.log.Printf("gateway: agent offline %s/%s", s.key.tenant, s.key.sip)
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
			g.log.Printf("gateway: agent %s/%s bad msg: %v", s.key.tenant, s.key.sip, err)
			continue
		}
		s.dispatch(m)
	}
}
