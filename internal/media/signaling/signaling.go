// Package signaling is the browser-facing /rtc WebSocket for the pion media
// plane: it authenticates the client, joins it to a room (internal/media/room)
// and a PeerConnection on the SFU (internal/media/sfu), relays trickle-ICE
// signaling, and fans room membership events out to the other participants.
//
// The SFU is the sole offerer: the server sends offers, the client answers, so
// the WS only ever carries client → answer/ice and server → offer/ice plus
// peer-* membership events. This package contains no FreeSWITCH/SIP concepts.
package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/brain"
	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/presence"
	"github.com/shridarpatil/tring/internal/routing"
	"github.com/shridarpatil/tring/internal/turn"
)

// Config wires the signaling server to its dependencies. The room Manager is
// built internally so its event emitter can fan out to connected sockets.
type Config struct {
	SFU       *sfu.SFU
	JWTSecret []byte
	// ICE servers handed to the browser in `joined`. STUNURL is advertised as-is;
	// when TURNSecret+TURNURL are set, an ephemeral coturn credential is minted
	// per join (lifetime TURNTTL).
	STUNURL    string
	TURNURL    string
	TURNSecret string
	TURNTTL    time.Duration
	// Grace is the room reconnect window (0 → room.DefaultGrace).
	Grace time.Duration
	// DevTokens mounts an UNAUTHENTICATED token-mint at /rtc/dev-token for the
	// local test client. Dev only — never enable in production.
	DevTokens bool
	// Flows loads an IVR flow definition by id (read-only). When set, a join
	// carrying flow_id spawns the IVR as a participant in that room. nil → web
	// calls are plain peer-to-peer (no IVR).
	Flows FlowLoader
	// Teams resolves a team's member SIPs so a transfer rings the team's agents
	// (not all online agents). nil → a team target rings all online agents.
	Teams TeamResolver
	// HoldMusic resolves a tenant's system hold music for the agent-initiated
	// hold path (msgHold). nil → agent hold plays silence. The queue/ring wait
	// gets its hold music from the team config, not this.
	HoldMusic HoldMusicResolver
	// TransferMessage resolves the tenant's blind-transfer no-answer prompt. nil →
	// a failed transfer hangs up the caller silently.
	TransferMessage TransferMessageResolver
	// AudioDir is where IVR prompt files (Ogg/Opus) are read from.
	AudioDir string
	// Tokens burns visitor gateway-call-token jtis single-use (anti-replay),
	// mirroring the gateway. Required to accept visitor tokens — a visitor token
	// with no store is rejected (fail-closed). nil → only agent tokens work.
	Tokens *cache.Cache
	// Presence publishes agent online/offline to the shared Redis store the API +
	// admin console read (the same store the legacy /agent/ws wrote). nil/disabled
	// → the dashboard won't show /rtc agents as online. Best-effort.
	Presence *presence.Store
	// Calls records CDRs + recordings for media-plane calls (one path for web +
	// phone). nil → no CDRs (dev / plain rooms).
	Calls CallLog
	// Cache publishes this edge's web team-queue depth to the shared Redis store
	// the API/dashboard sums (alongside the orchestrator's phone depth). nil →
	// web queue waiters won't show on the dashboard (queueing still works).
	Cache *cache.Cache
	// Events publishes dashboard live-update events (presence, queue depth) to
	// the shared Redis bus the api Hub subscribes to. nil = events disabled.
	Events events.Sink
	// Supervisors records supervisor session audit (internal/callcdr implements
	// it). nil → no audit. Best-effort.
	Supervisors SupervisorLog
	// Originator places agent-initiated outbound PSTN calls (internal/fsplug
	// implements it). nil → outbound dialing (msgDial) is disabled.
	Originator Originator
	// Trunks resolves a tenant's already-selected ordered outbound trunks (the
	// same selection internal/api uses for FreeSWITCH-path calls). nil →
	// outbound dialing is disabled.
	Trunks TrunkResolver
	// HTTP is the client IVR http_callback nodes call tenant URLs with. Must be
	// SSRF-guarded (internal/safehttp); nil → the guarded default with no
	// private-range allowlist.
	HTTP   *http.Client
	Logger *slog.Logger
}

// FlowLoader loads a tenant's parsed IVR flow by id (internal/flowstore
// implements it). Both calls are tenant-scoped: a flow id from another tenant
// must not resolve.
// FlowName resolves just the flow's display name (cheap, reuses the same row) so
// the web CDR can log the dialed flow as the call's to_number — it works whether
// or not the flow graph itself is loaded in this process (brain-up vs fallback).
type FlowLoader interface {
	Load(ctx context.Context, tenant, flowID string) (*ivr.Flow, error)
	FlowName(ctx context.Context, tenant, flowID string) (string, error)
}

// TeamResolver returns a team's member SIP usernames and its queue config
// (internal/flowstore implements both via internal/routing). Lets the transfer
// ring target a team's agents and honor its strategy / ring_seconds / max_wait.
type TeamResolver interface {
	TeamSIPs(ctx context.Context, tenantID uuid.UUID, team string) ([]string, error)
	TeamConfig(ctx context.Context, tenantID uuid.UUID, team string) (routing.TeamConfig, bool, error)
}

// CallLog records CDRs + recordings for media-plane calls (internal/callcdr
// implements it). Started/Answered return a TrackSink to attach to that leg's
// publication when the tenant records calls (nil → no capture). All methods are
// best-effort and idempotent; a nil CallLog disables CDRs entirely.
type CallLog interface {
	Started(ctx context.Context, tenant uuid.UUID, roomID, from, to, source, clientRef string, callerPID room.ParticipantID) room.TrackSink
	// StartedOutbound is Started's sibling for agent-initiated outbound calls
	// (internal/media/signaling/outbound.go) — it records direction='outbound'
	// (source is always 'phone' for this path, so there's no source param),
	// instead of Started's hardcoded 'inbound'.
	StartedOutbound(ctx context.Context, tenant uuid.UUID, roomID, from, to, clientRef string, callerPID room.ParticipantID) room.TrackSink
	Answered(roomID string, agentPID room.ParticipantID, agentSip string) room.TrackSink
	// RingStart/RingStop drive the live-dashboard "ringing → <target>" phase for a
	// transfer: RingStart when routing to a team/agent begins (so the row shows who
	// it's ringing instead of the IVR label), RingStop when the ring ends with no
	// answer (revert to the IVR phase). Dashboard-only, best-effort.
	RingStart(ctx context.Context, roomID, team, agentSIP string)
	// RingAgent records that one specific agent is now being rung (id + name), so
	// the timeline names who the call rang. Called per agent as each is popped.
	RingAgent(roomID, sip string)
	RingStop(roomID string)
	// Transferred records an agent-initiated blind transfer on the call timeline
	// (the one routing event outside the IVR walk), at initiation before the target
	// rings. Best-effort.
	Transferred(roomID, from, to string)
	Journey(roomID string, steps []ivr.Step)
	Ended(roomID, cause string)
}

// Server is the /rtc signaling surface.
type Server struct {
	cfg   Config
	sfu   *sfu.SFU
	rooms *room.Manager
	log   *slog.Logger

	// joinTimeout bounds the wait for a socket's first message (defaultJoinTimeout;
	// a field so tests can shorten it without racing live handlers).
	joinTimeout time.Duration

	// brain drives IVR sessions in-process — the brain is embedded in this server.
	// One Runner shared across all rooms; state per-room inside the Runner.
	brain *brain.Runner

	// pstnGW is the phone plug (room.Plug — FS today, swappable); nil
	// when no phone side is configured. pstnDID resolves a dialed DID to its
	// tenant + flow. See pstn.go.
	pstnGW  room.Plug
	pstnDID DIDResolver

	mu           sync.Mutex
	conns        map[room.ParticipantID]*conn
	agents       map[string]map[string]*conn                          // online agents: tenant → sip → presence socket
	rings        map[room.RoomID]chan struct{}                        // pending transfer rings → closed when an agent joins
	declines     map[room.RoomID]func(sip string)                     // registered per active ring → an agent rejecting advances the router now
	pstnCalls    map[room.RoomID]room.CallID                          // live phone legs by room (pstn.go)
	outboundLegs map[room.RoomID]room.ParticipantID                   // agent-initiated outbound legs by room (outbound.go)
	onCall       map[string]int                                       // "tenant|sip" → active call legs (drives on_call presence)
	callers      map[room.RoomID]room.ParticipantID                   // the IVR caller per room (flow-pinned joiner) — see handoff.go
	handoffs     map[room.RoomID]*handoff                             // rooms waiting for a transfer handoff to end (handoff.go)
	holds        map[room.RoomID]func()                               // rooms with agent-initiated hold music playing → stop func (holdmusic.go)
	transfers    map[room.RoomID]context.CancelFunc                   // rooms with a blind transfer ringing → cancel (transfer.go)
	roles        map[room.ParticipantID]partRole                      // signaling-only participant roles (supervisor.go)
	supers       map[room.RoomID]map[room.ParticipantID]*superSession // supervisor sessions per room (supervisor.go)
	hangupBy     map[room.RoomID]string                               // who ended the call ("client"/"agent"); first Bye wins, "system" if unset

	// router runs the shared queue/ring discipline (internal/routing) — the same
	// engine the FreeSWITCH plane uses, so web transfers honor team strategy,
	// max_wait, reservations and queue depth identically. The pion plane supplies
	// the transport via a pionRing Effects adapter (see agents.go).
	router *routing.Router

	// agentRouter is the brain-owned ivrdrive.Agents implementation: it drives
	// team-resolution → RouteConfig → routing.Router.Run, delegating back to
	// Server.Prepare for the pion-specific transport pieces.
	agentRouter *brain.AgentRouter
}

// New builds a signaling Server. The room Manager's emitter routes membership
// events to the relevant sockets (and tears down a left participant's SFU peer).
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		cfg:          cfg,
		sfu:          cfg.SFU,
		log:          cfg.Logger.With("component", "signaling"),
		joinTimeout:  defaultJoinTimeout,
		brain:        brain.NewRunner(cfg.Logger),
		conns:        make(map[room.ParticipantID]*conn),
		agents:       make(map[string]map[string]*conn),
		rings:        make(map[room.RoomID]chan struct{}),
		declines:     make(map[room.RoomID]func(sip string)),
		pstnCalls:    make(map[room.RoomID]room.CallID),
		outboundLegs: make(map[room.RoomID]room.ParticipantID),
		onCall:       make(map[string]int),
		callers:      make(map[room.RoomID]room.ParticipantID),
		handoffs:     make(map[room.RoomID]*handoff),
		holds:        make(map[room.RoomID]func()),
		transfers:    make(map[room.RoomID]context.CancelFunc),
	}
	// Build the shared routing engine from Redis-backed backends when a cache is
	// wired (so the queue/reservations/depth line up with the FreeSWITCH plane on
	// the same Redis), else in-memory. presence.New(nil) is a disabled no-op.
	var queue routing.Queue
	var reserve routing.Reservations
	if cfg.Cache != nil {
		queue, reserve = routing.NewRedisQueue(cfg.Cache), routing.NewRedisReservations(cfg.Cache)
	} else {
		queue, reserve = routing.NewMemQueue(), routing.NewMemReservations()
	}
	s.router = routing.NewRouter(queue, reserve, routing.NewMemRR(), cfg.Presence, cfg.Cache, cfg.Events, s.log)
	s.agentRouter = brain.NewAgentRouter(cfg.Teams, s.router, s, s.log)
	s.rooms = room.NewManager(room.Options{Grace: cfg.Grace, Emitter: s.routeEvent})
	return s
}

// Handler is the HTTP surface: the /rtc WebSocket plus a health check. CORS is
// permissive so the browser SDK on another origin can connect.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/rtc", s.handleWS)
	if s.cfg.DevTokens {
		s.log.Warn("rtc dev-token mint ENABLED at /rtc/dev-token — dev only, never in production")
		mux.HandleFunc("/rtc/dev-token", s.handleDevToken)
	}
	return cors(mux)
}

// handleWS authenticates the agent token, upgrades the socket, performs the
// join handshake, and runs the per-connection read loop until close.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	raw := bearer(r)
	if raw == "" {
		raw = r.URL.Query().Get("token")
	}
	princ, err := validatePrincipal(raw, s.cfg.JWTSecret)
	if err != nil {
		http.Error(w, `{"error":"invalid or missing token"}`, http.StatusUnauthorized)
		return
	}
	tenant := princ.tenant.String()

	netConn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		s.log.Error("ws upgrade failed", "err", err)
		return
	}
	c := &conn{Conn: netConn}

	// First message decides the socket's role: `register` = an available agent
	// (presence, can be rung into a call); `join` = a call participant. Bound
	// the wait so an authenticated-but-silent socket can't pin this goroutine;
	// each role's read loop sets its own deadline from here on.
	_ = netConn.SetReadDeadline(time.Now().Add(s.joinTimeout))
	first, err := c.read()
	if err != nil {
		_ = netConn.Close()
		return
	}
	if princ.isSupervisor {
		// A supervisor never registers as an agent or joins as a room.Manager
		// participant — it's an SFU-only observer. Handled entirely separately.
		s.handleSupervisor(c, princ, first, tenant)
		return
	}
	if first.Type == msgRegister {
		if princ.isVisitor {
			_ = c.send(msg{Type: msgError, Reason: "visitor token cannot register as agent"})
			_ = netConn.Close()
			return
		}
		s.handleAgentPresence(c, tenant, princ.sip)
		return
	}
	if first.Type != msgJoin {
		_ = c.send(msg{Type: msgError, Reason: "expected join or register"})
		_ = netConn.Close()
		return
	}
	if first.Room == "" {
		_ = c.send(msg{Type: msgError, Reason: "room required"})
		_ = netConn.Close()
		return
	}
	// Namespace the room by tenant so two tenants can't share a room id.
	roomID := room.RoomID(tenant + "/" + first.Room)
	// A visitor's flow is pinned by the TOKEN (server-side), never the join.
	flowID := first.FlowID
	if princ.isVisitor {
		flowID = princ.flowID
	}

	var p *room.Participant
	freshJoin := true
	if first.Token != "" {
		if p, _ = s.rooms.Reattach(roomID, first.Token); p != nil {
			freshJoin = false
		}
	}
	if p == nil { // fresh join, or reattach failed (expired) → new slot
		// Burn a visitor token's jti once, on the FIRST (non-reattach) join, so a
		// captured token can't be amplified into many calls. Fails closed.
		if princ.isVisitor {
			if err := s.consumeVisitorToken(r.Context(), princ.jti, princ.exp); err != nil {
				s.log.Warn("visitor token rejected", "err", err)
				_ = c.send(msg{Type: msgError, Reason: "token rejected"})
				_ = netConn.Close()
				return
			}
		}
		p, err = s.rooms.Join(roomID, first.Name)
	}
	if err != nil || p == nil {
		_ = c.send(msg{Type: msgError, Reason: "join failed"})
		_ = netConn.Close()
		return
	}
	c.pid = p.ID()

	role := roleAgent
	if princ.isVisitor {
		role = roleVisitor
	}
	s.setRole(p.ID(), role)

	s.mu.Lock()
	s.conns[p.ID()] = c
	s.mu.Unlock()

	peer, err := s.sfu.Join(roomID, p.ID(), c)
	if err != nil {
		s.log.Error("sfu join failed", "err", err, "room", roomID)
		s.dropConn(p.ID())
		p.Leave()
		_ = netConn.Close()
		return
	}
	c.peer = peer
	peer.OnConnected = func() { p.MarkConnected() }
	peer.OnClosed = func() { p.Drop() } // grace window; the left event tears down the SFU peer

	// Tell the joiner who it is + how to reach ICE, then who's already here.
	_ = c.send(msg{
		Type:        msgJoined,
		Participant: string(p.ID()),
		Token:       p.Token(),
		ICEServers:  s.iceServers(tenant),
	})
	if r, ok := s.rooms.Get(roomID); ok {
		for _, other := range r.Participants() {
			if other.ID() != p.ID() {
				_ = c.send(msg{Type: msgPeerJoined, Participant: string(other.ID()), Name: other.Name()})
			}
		}
	}

	// Kick off the initial server offer.
	peer.Negotiate()

	// An agent joining a call a supervisor is already watching (a phone call is
	// often watched from its IVR) must hear a whisper in progress and see the
	// supervisor indicator. Also re-sends the indicator after a page refresh.
	if role == roleAgent && s.supervisorsRemain(roomID) {
		s.syncSupervisors(roomID)
	}

	// Flow-pinned room → run the IVR as a participant (once per room). The dev
	// flow ("dev", when dev tokens are on) needs no DB — it just rings agents.
	if flowID != "" {
		if s.cfg.Flows != nil || (s.cfg.DevTokens && flowID == devFlowID) {
			// Record the caller so a later transfer handoff can tell the caller apart
			// from the answering agent (post-call IVR runs to the caller). See handoff.go.
			s.setCaller(roomID, p.ID())
			s.startIVR(roomID, flowID, princ.clientRef, map[string]string{"caller": princ.clientRef}) // brain runs the IVR in-process
		}
	}
	// A flow-pinned FRESH join is a web CALL — open its CDR (and attach the
	// caller's recording leg when the tenant records). Reattaches (page refresh)
	// keep the original CDR; plain rooms (no flow) get none.
	if flowID != "" && freshJoin && s.cfg.Calls != nil {
		from := princ.clientRef
		if from == "" {
			from = first.Name
		}
		if from == "" {
			from = "Web visitor" // anonymous caller has no identity — don't log blank
		}
		// to_number is the dialed destination: the IVR flow's name (best-effort —
		// falls back to a literal if the flow can't be resolved). source='web'
		// distinguishes the channel without overloading to_number.
		flowName := "Web call"
		if s.cfg.Flows != nil {
			if n, err := s.cfg.Flows.FlowName(r.Context(), princ.tenant.String(), flowID); err == nil && n != "" {
				flowName = n
			}
		}
		if sink := s.cfg.Calls.Started(r.Context(), princ.tenant, string(roomID), from, flowName, "web", princ.clientRef, p.ID()); sink != nil {
			s.sfu.AddSink(roomID, p.ID(), sink)
		}
	}
	// A non-visitor (agent) joining an existing call room is answering it: mark the
	// CDR answered + attribute the agent (by their JWT SIP), and attach their
	// recording leg. princ.sip is available here (unlike in routeEvent, which fires
	// mid-Join before s.conns is set). callcdr no-ops on a non-call room or the
	// caller's own join.
	agentCallLeg := flowID == "" && !princ.isVisitor
	if agentCallLeg && s.cfg.Calls != nil {
		if sink := s.cfg.Calls.Answered(string(roomID), p.ID(), princ.sip); sink != nil {
			s.sfu.AddSink(roomID, p.ID(), sink)
		}
	}
	// Reflect agent busy/available in presence so the dashboard shows "on call".
	// Cleared when this call leg's socket closes (handleWS returns after readLoop).
	if agentCallLeg {
		s.setAgentOnCall(tenant, princ.sip, true)
		defer s.setAgentOnCall(tenant, princ.sip, false)
	}

	// If a transfer is ringing for this room, this join is the agent answering:
	// signalRing resolves the pending ring.
	s.signalRing(roomID)

	s.log.Info("participant joined", "room", roomID, "participant", p.ID())
	s.readLoop(c, p, princ.tenant, !princ.isVisitor, princ.sip)
}

// readLoop dispatches client messages until the socket closes, then drops the
// participant into the grace window. tenant is the joiner's principal tenant
// (needed for agent-initiated actions like msgDial that must resolve
// tenant-scoped config, e.g. outbound trunks). isAgent gates agent-only
// actions (msgDial) — a visitor socket also carries a tenant, but must never
// be able to originate a billable PSTN call on it (toll fraud).
func (s *Server) readLoop(c *conn, p *room.Participant, tenant uuid.UUID, isAgent bool, sip string) {
	defer func() {
		// Only act if THIS socket is still the participant's current one. A reattach
		// (page refresh) replaces conns[pid] with the new socket; when this (old)
		// readLoop then exits it must NOT close the new socket or drop the shared
		// slot. Identity check makes teardown order-independent.
		s.mu.Lock()
		current := s.conns[p.ID()] == c
		if current {
			delete(s.conns, p.ID())
		}
		s.mu.Unlock()
		_ = c.Close()
		if current {
			p.Drop() // live socket lost → reconnecting; grace expiry (or bye) finalizes teardown
		}
	}()
	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		m, err := c.read()
		if err != nil {
			return
		}
		switch m.Type {
		case msgAnswer:
			if c.peer != nil {
				if err := c.peer.HandleAnswer(m.SDP); err != nil {
					s.log.Warn("handle answer", "err", err, "participant", p.ID())
				}
			}
		case msgICE:
			if c.peer != nil && len(m.Candidate) > 0 {
				if init := webrtcICE(m.Candidate); init != nil {
					if err := c.peer.HandleCandidate(*init); err != nil {
						s.log.Warn("handle candidate", "err", err, "participant", p.ID())
					}
				}
			}
		case msgPublish:
			if c.peer != nil {
				c.peer.Publish(codecKind(m.Kind))
			}
		case msgUnpublish:
			s.sfu.Unpublish(p.Room().ID(), p.ID(), codecKind(m.Kind))
		case msgDTMF:
			s.routeDTMF(p.Room().ID(), m.Digit)
		case msgPing:
			// Keepalive — the read above already reset the deadline; nothing to do.
			// A quiet call (media rides RTP, not this socket) would otherwise hit the
			// 90s read deadline and get dropped mid-call.
		case msgHold:
			// The agent muted its own mic client-side; play the tenant's hold music
			// into the room so the held party hears music, not dead air — and deafen
			// the agent, which must hear neither that music nor the held party.
			s.holdParty(p.Room().ID(), p.ID())
		case msgResume:
			s.resumeParty(p.Room().ID())
		case msgTransfer:
			if !isAgent {
				_ = c.send(msg{Type: msgTransferFailed, Reason: "only an agent can transfer"})
				continue
			}
			go s.handleTransfer(c, p, tenant, sip, m)
		case msgDial:
			// Agent-initiated outbound PSTN call into this (already joined) room.
			// Visitor sockets carry a tenant too (for CDR/token purposes) but must
			// NEVER be able to originate a billable PSTN call on it — toll fraud.
			if !isAgent {
				_ = c.send(msg{Type: msgError, Reason: "outbound calling is agent-only"})
				continue
			}
			go s.handleAgentDial(p.Room().ID(), tenant, p.ID(), m.To)
		case msgBye:
			// An explicit hangup — attribute the call's end to this party (first Bye wins).
			s.markHangupInitiator(p.Room().ID(), p.ID())
			p.Leave() // terminal — emits left, which tears down the SFU peer
			return
		default:
			s.log.Warn("unknown message", "type", m.Type, "participant", p.ID())
		}
	}
}

// markHangupInitiator records WHO ended a call, from the explicit `bye` a party's
// SDK sends on hangup (and on the peer-left cascade). First writer wins, so the
// party who actually hung up is captured — not the other side that auto-leaves
// after. Supervisors and unknown roles never count.
func (s *Server) markHangupInitiator(rid room.RoomID, pid room.ParticipantID) {
	s.setHangupBy(rid, s.hangupRole(pid))
}

// hangupRole maps a participant to the cause it is attributed as when it ends a
// call: "client" (visitor) or "agent"; "" for supervisors / unknown roles.
func (s *Server) hangupRole(pid room.ParticipantID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.roles[pid] {
	case roleVisitor:
		return "client"
	case roleAgent:
		return "agent"
	}
	return ""
}

// setHangupBy records a room's hangup cause if none is recorded yet (first
// writer wins). "" is a no-op. Server-initiated ends (flow end, IVR hangup node,
// no-answer) record "system" just before they tear the call down.
func (s *Server) setHangupBy(rid room.RoomID, by string) {
	if by == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hangupBy == nil {
		s.hangupBy = map[room.RoomID]string{}
	}
	if _, ok := s.hangupBy[rid]; !ok {
		s.hangupBy[rid] = by
	}
}

// clearHangupBy forgets a recorded cause when the call carries on after that
// party left (an agent leaving into a post-call flow or a blind transfer).
func (s *Server) clearHangupBy(rid room.RoomID) {
	s.mu.Lock()
	delete(s.hangupBy, rid)
	s.mu.Unlock()
}

// endCause resolves and consumes a room's hangup cause: the recorded initiator
// ("client"/"agent"/"system"), or "system" when nothing was recorded.
func (s *Server) endCause(rid room.RoomID) string {
	return s.endCauseOr(rid, "system")
}

// endCauseOr is endCause with the fallback for an unrecorded room — e.g. a
// phone leg that ended on its own is the caller hanging up. Clears the entry.
func (s *Server) endCauseOr(rid room.RoomID, def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	by, ok := s.hangupBy[rid]
	delete(s.hangupBy, rid)
	if !ok {
		return def
	}
	return by
}

// routeEvent is the room Manager's emitter: it fans membership events out to the
// OTHER participants' sockets, and finalizes a left participant by tearing down
// its SFU peer.
func (s *Server) routeEvent(e room.Event) {
	// NOTE: the "agent answered" CDR + recording-sink wiring lives in handleWS
	// (where the agent's JWT SIP is known), not here — this emitter fires DURING
	// rooms.Join, before s.conns/identity is set.
	if e.Type == room.EventParticipantLeft {
		s.sfu.Leave(e.Room, e.Participant)
		s.dropConn(e.Participant)
		// Resolve the leaver's role before clearing it: a party that left WITHOUT a
		// bye (closed tab, network drop past the grace window) still ended the call.
		leaverBy := s.hangupRole(e.Participant)
		s.clearRole(e.Participant)
		// A held call is a 1:1 state between the two live parties: if either leaves,
		// stop any agent-initiated hold music so its source never outlives the call.
		s.clearHold(e.Room)
		// If no human participants remain, end the call: stop the IVR — and if a
		// phone leg is attached with nobody serving it, hang it up (pstn.go).
		if r, ok := s.rooms.Get(e.Room); !ok || len(r.Participants()) == 0 {
			s.setHangupBy(e.Room, leaverBy) // no-op if a bye / system end was recorded first
			s.endTransfer(e.Room) // caller gone → stop any in-flight blind-transfer ring
			s.stopIVR(e.Room)
			s.hangupPstnIfAbandoned(e.Room)
			s.hangupOutboundIfAbandoned(e.Room)
			s.closeSupervisors(e.Room) // end any supervisor sessions with the call
			if s.cfg.Calls != nil {
				s.cfg.Calls.Ended(string(e.Room), s.endCause(e.Room))
			}
		} else if s.resumeHandoffIfAgentLeft(e.Room, e.Participant) {
			// A transfer's answered agent just left with the caller still on the line:
			// resume the IVR so a node wired to the transfer's "completed" edge runs
			// to the caller (post-call survey). Do NOT end the caller — and do NOT
			// fan out peer-left to the caller: the visitor SDK treats "the only peer
			// left" as call-over (sends Bye), which would tear the caller down before
			// the post-call flow can play. The SFU already removed the agent's track
			// (renegotiation), so the caller simply hears the IVR again. The call goes
			// on, so the agent's bye no longer decides who ends it.
			s.clearHangupBy(e.Room)
			return
		} else if s.transferActive(e.Room) {
			// A blind transfer is ringing: the initiating agent just left and the
			// caller is briefly alone. Keep the room (the target will join, or the
			// no-answer message plays) and suppress peer-left to the caller — the
			// visitor SDK treats "the last peer left" as call-over and would Bye.
			s.clearHangupBy(e.Room)
			return
		} else {
			// A 1:1 call whose OTHER party just left: end the lone remaining leg now
			// rather than wait for it to notice and tear down. That teardown closes
			// its socket WITHOUT a Bye, so it would otherwise sit in the 30s grace
			// window before the room empties + the CDR settles (stale "on call").
			// endIfAlone no-ops on a phone room (the remaining leg is the agent
			// serving the port caller) or when ≥2 participants remain. It removes the
			// leg via Leave(), which re-enters here with the room now empty → Ended.
			// When it does end the call, the party who just left is who hung up.
			if r, ok := s.rooms.Get(e.Room); ok && len(r.Participants()) == 1 && !s.sfu.HasPort(e.Room) {
				s.setHangupBy(e.Room, leaverBy)
			}
			s.endIfAlone(e.Room)
		}
	}

	var out msg
	switch e.Type {
	case room.EventParticipantJoined:
		out = msg{Type: msgPeerJoined, Participant: string(e.Participant), Name: e.Name}
	case room.EventParticipantState:
		out = msg{Type: msgPeerState, Participant: string(e.Participant), State: e.State.String()}
	case room.EventParticipantLeft:
		out = msg{Type: msgPeerLeft, Participant: string(e.Participant)}
	default:
		return
	}

	r, ok := s.rooms.Get(e.Room)
	if !ok {
		return
	}
	for _, p := range r.Participants() {
		if p.ID() == e.Participant {
			continue
		}
		if c := s.connFor(p.ID()); c != nil {
			_ = c.send(out)
		}
	}
}

func (s *Server) connFor(pid room.ParticipantID) *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[pid]
}

func (s *Server) dropConn(pid room.ParticipantID) {
	s.mu.Lock()
	c := s.conns[pid]
	delete(s.conns, pid)
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// iceServers builds the browser-facing ICE server list: the STUN URL plus, when
// a TURN secret is configured, an ephemeral coturn credential.
func (s *Server) iceServers(userid string) []ICEServer {
	var out []ICEServer
	if s.cfg.STUNURL != "" {
		out = append(out, ICEServer{URLs: []string{s.cfg.STUNURL}})
	}
	if s.cfg.TURNURL != "" && s.cfg.TURNSecret != "" {
		ttl := s.cfg.TURNTTL
		if ttl <= 0 {
			ttl = time.Hour
		}
		user, cred := turn.Credential(s.cfg.TURNSecret, userid, ttl)
		out = append(out, ICEServer{URLs: []string{s.cfg.TURNURL}, Username: user, Credential: cred})
	}
	return out
}

// conn is one client WebSocket. It implements sfu.Signaler (SendOffer /
// SendCandidate) and serializes writes.
type conn struct {
	net.Conn
	pid     room.ParticipantID
	peer    *sfu.Peer
	writeMu sync.Mutex
}

// defaultJoinTimeout bounds the wait for a socket's first (join/register) message.
const defaultJoinTimeout = 10 * time.Second

// maxWSMessageBytes caps one client message. The largest legitimate one is an
// SDP answer (a few KiB, more with screen share); 64 KiB leaves ample room.
const maxWSMessageBytes = 64 << 10

var errMessageTooLarge = errors.New("ws message too large")

func (c *conn) read() (msg, error) {
	data, err := readClientText(c.Conn, maxWSMessageBytes)
	if err != nil {
		return msg{}, err
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		return msg{}, err
	}
	return m, nil
}

// readClientText is wsutil.ReadClientText with a size cap: a frame whose header
// declares more than max bytes is refused before reading it, and fragments
// can't add up past max either. Binary messages are discarded; control frames
// (ping/close) are answered as before.
func readClientText(rw io.ReadWriter, max int64) ([]byte, error) {
	ctl := wsutil.ControlFrameHandler(rw, ws.StateServerSide)
	rd := wsutil.Reader{
		Source:         rw,
		State:          ws.StateServerSide,
		CheckUTF8:      true,
		MaxFrameSize:   max,
		OnIntermediate: ctl,
	}
	for {
		hdr, err := rd.NextFrame()
		if err != nil {
			return nil, err
		}
		if hdr.OpCode.IsControl() {
			if err := ctl(hdr, &rd); err != nil {
				return nil, err
			}
			continue
		}
		if hdr.OpCode&ws.OpText == 0 {
			if err := rd.Discard(); err != nil {
				return nil, err
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(&rd, max+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > max {
			return nil, errMessageTooLarge
		}
		return data, nil
	}
}

func (c *conn) send(m msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsutil.WriteServerMessage(c.Conn, ws.OpText, b)
}

// SendOffer implements sfu.Signaler.
func (c *conn) SendOffer(sdp string) error { return c.send(msg{Type: msgOffer, SDP: sdp}) }

// SendCandidate implements sfu.Signaler.
func (c *conn) SendCandidate(cand webrtc.ICECandidateInit) error {
	b, err := json.Marshal(cand)
	if err != nil {
		return err
	}
	return c.send(msg{Type: msgICE, Candidate: b})
}

// cors echoes the request Origin so the browser SDK (cross-origin) can connect.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// webrtcICE decodes a trickle-ICE candidate payload, or nil if malformed.
func webrtcICE(raw json.RawMessage) *webrtc.ICECandidateInit {
	var init webrtc.ICECandidateInit
	if json.Unmarshal(raw, &init) != nil {
		return nil
	}
	return &init
}

// codecKind maps a publish/unpublish kind string to a pion codec type
// (defaults to video, the only extra track in M5 — screen share).
func codecKind(kind string) webrtc.RTPCodecType {
	if kind == "audio" {
		return webrtc.RTPCodecTypeAudio
	}
	return webrtc.RTPCodecTypeVideo
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}
