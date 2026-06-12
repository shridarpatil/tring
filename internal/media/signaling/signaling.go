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

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/media/sfu"
	"github.com/whatomate/tring/internal/turn"
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
	// AudioDir is where IVR prompt files (Ogg/Opus) are read from.
	AudioDir string
	Logger   *slog.Logger
}

// FlowLoader loads a parsed IVR flow by id (internal/flowstore implements it).
type FlowLoader interface {
	Load(ctx context.Context, flowID string) (*ivr.Flow, error)
}

// TeamResolver returns a team's member SIP usernames (internal/flowstore
// implements it). Lets the transfer ring target a team's agents.
type TeamResolver interface {
	TeamSIPs(ctx context.Context, tenantID uuid.UUID, team string) ([]string, error)
}

// Server is the /rtc signaling surface.
type Server struct {
	cfg   Config
	sfu   *sfu.SFU
	rooms *room.Manager
	log   *slog.Logger

	// control is the brain↔edge channel (Phase 2). When set + connected, the
	// orchestrator runs the IVR remotely: this edge sends call.started/dtmf/
	// call.ended UP and executes play/ring/ivr.stop commands DOWN instead of
	// walking the flow locally. nil/disconnected → local IVR fallback.
	control controlChannel

	mu     sync.Mutex
	conns  map[room.ParticipantID]*conn
	ivr    map[room.RoomID]*ivrSession   // local IVR per room (fallback / control off)
	edge   map[room.RoomID]*edgeVoice    // brain-driven IVR voice per room (control on)
	agents map[string]map[string]*conn   // online agents: tenant → sip → presence socket
	rings  map[room.RoomID]chan struct{} // pending transfer rings → closed when an agent joins
}

// New builds a signaling Server. The room Manager's emitter routes membership
// events to the relevant sockets (and tears down a left participant's SFU peer).
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		cfg:    cfg,
		sfu:    cfg.SFU,
		log:    cfg.Logger.With("component", "signaling"),
		conns:  make(map[room.ParticipantID]*conn),
		ivr:    make(map[room.RoomID]*ivrSession),
		edge:   make(map[room.RoomID]*edgeVoice),
		agents: make(map[string]map[string]*conn),
		rings:  make(map[room.RoomID]chan struct{}),
	}
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
	cl, err := validateToken(raw, s.cfg.JWTSecret)
	if err != nil {
		http.Error(w, `{"error":"invalid or missing token"}`, http.StatusUnauthorized)
		return
	}
	tenant := cl.TenantID.String()

	netConn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		s.log.Error("ws upgrade failed", "err", err)
		return
	}
	c := &conn{Conn: netConn}

	// First message decides the socket's role: `register` = an available agent
	// (presence, can be rung into a call); `join` = a call participant.
	first, err := c.read()
	if err != nil {
		_ = netConn.Close()
		return
	}
	if first.Type == msgRegister {
		s.handleAgentPresence(c, tenant, cl.SipUsername)
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

	var p *room.Participant
	if first.Token != "" {
		p, err = s.rooms.Reattach(roomID, first.Token)
	}
	if p == nil { // fresh join, or reattach failed (expired) → new slot
		p, err = s.rooms.Join(roomID, first.Name)
	}
	if err != nil || p == nil {
		_ = c.send(msg{Type: msgError, Reason: "join failed"})
		_ = netConn.Close()
		return
	}
	c.pid = p.ID()

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

	// Flow-pinned room → run the IVR as a participant (once per room). The dev
	// flow ("dev", when dev tokens are on) needs no DB — it just rings agents.
	if first.FlowID != "" {
		if s.controlUp() {
			// Brain runs the IVR: announce the call; the orchestrator drives prompts
			// and the ring back over the control channel (Phase 2b).
			_ = s.control.Send(control.Message{
				Type:        control.EvCallStarted,
				Tenant:      tenant,
				Room:        string(roomID),
				Participant: string(p.ID()),
				FlowID:      first.FlowID,
				Name:        first.Name,
			})
		} else if s.cfg.Flows != nil || (s.cfg.DevTokens && first.FlowID == devFlowID) {
			s.startIVR(roomID, first.FlowID) // local fallback (control down)
		}
	}
	// If a transfer is ringing for this room, this join is the agent answering.
	// Local fallback resolves it via signalRing; with control up, tell the brain
	// (it owns the pending ring) via participant.joined.
	s.signalRing(roomID)
	if s.controlUp() {
		_ = s.control.Send(control.Message{
			Type: control.EvParticipantJoined, Room: string(roomID),
			Participant: string(p.ID()), Name: first.Name,
		})
	}

	s.log.Info("participant joined", "room", roomID, "participant", p.ID())
	s.readLoop(c, p)
}

// readLoop dispatches client messages until the socket closes, then drops the
// participant into the grace window.
func (s *Server) readLoop(c *conn, p *room.Participant) {
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
				var init webrtc.ICECandidateInit
				if json.Unmarshal(m.Candidate, &init) == nil {
					if err := c.peer.HandleCandidate(init); err != nil {
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
			if s.controlUp() {
				_ = s.control.Send(control.Message{
					Type: control.EvDTMF, Room: string(p.Room().ID()),
					Participant: string(p.ID()), Digit: m.Digit,
				})
			} else {
				s.routeDTMF(p.Room().ID(), m.Digit)
			}
		case msgBye:
			p.Leave() // terminal — emits left, which tears down the SFU peer
			return
		default:
			s.log.Warn("unknown message", "type", m.Type, "participant", p.ID())
		}
	}
}

// routeEvent is the room Manager's emitter: it fans membership events out to the
// OTHER participants' sockets, and finalizes a left participant by tearing down
// its SFU peer.
func (s *Server) routeEvent(e room.Event) {
	if e.Type == room.EventParticipantLeft {
		s.sfu.Leave(e.Room, e.Participant)
		s.dropConn(e.Participant)
		// If no human participants remain, end the call: tell the brain (it cancels
		// its walk) and drop the edge voice; also stop any local IVR.
		if r, ok := s.rooms.Get(e.Room); !ok || len(r.Participants()) == 0 {
			if s.controlUp() {
				_ = s.control.Send(control.Message{Type: control.EvCallEnded, Room: string(e.Room)})
				s.edgeStopVoice(e.Room)
			}
			s.stopIVR(e.Room)
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

func (c *conn) read() (msg, error) {
	data, err := wsutil.ReadClientText(c.Conn)
	if err != nil {
		return msg{}, err
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		return msg{}, err
	}
	return m, nil
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
