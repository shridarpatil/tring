package control

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Server is the BRAIN side of the control channel: it accepts authenticated edge
// connections, delivers received EVENTS to onEvent, and sends COMMANDS down. One
// orchestrator typically serves one gateway (per-FreeSWITCH-instance), but it
// supports several edges; a command is sent to every connected edge (room
// affinity is a 2b refinement once the brain tracks room→edge from call.started).
type Server struct {
	secret  []byte
	log     *slog.Logger
	onEvent func(Message)

	mu    sync.Mutex
	edges map[*edgeConn]struct{}
}

type edgeConn struct {
	net.Conn
	writeMu sync.Mutex
}

func (e *edgeConn) send(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return wsutil.WriteServerText(e.Conn, b)
}

// NewServer builds the brain-side control server. onEvent is called for every
// event an edge sends (nil → events are dropped). A nil/empty secret disables
// auth (dev only).
func NewServer(secret string, onEvent func(Message), log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if onEvent == nil {
		onEvent = func(Message) {}
	}
	return &Server{
		secret:  []byte(secret),
		log:     log.With("component", "control-server"),
		onEvent: onEvent,
		edges:   make(map[*edgeConn]struct{}),
	}
}

// Handler upgrades an authenticated edge WebSocket and runs its read loop.
func (s *Server) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		netConn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			s.log.Error("control upgrade failed", "err", err)
			return
		}
		ec := &edgeConn{Conn: netConn}
		s.add(ec)
		s.log.Info("edge connected", "remote", r.RemoteAddr)
		defer func() {
			s.remove(ec)
			_ = netConn.Close()
			s.log.Info("edge disconnected", "remote", r.RemoteAddr)
		}()
		for {
			data, err := wsutil.ReadClientText(netConn)
			if err != nil {
				return
			}
			var m Message
			if err := json.Unmarshal(data, &m); err != nil {
				s.log.Warn("control: bad event json", "err", err)
				continue
			}
			s.onEvent(m)
		}
	}
}

// Send delivers a command to every connected edge. Returns the number of edges
// it reached (0 = no edge connected — the caller should treat the command as a
// no-op / the call isn't on the media plane).
func (s *Server) Send(m Message) int {
	s.mu.Lock()
	targets := make([]*edgeConn, 0, len(s.edges))
	for e := range s.edges {
		targets = append(targets, e)
	}
	s.mu.Unlock()
	n := 0
	for _, e := range targets {
		if err := e.send(m); err != nil {
			s.log.Warn("control: send failed", "type", m.Type, "err", err)
			continue
		}
		n++
	}
	return n
}

// Connected reports whether at least one edge is attached.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.edges) > 0
}

// CloseEdges drops every connected edge; they reconnect on their own. Useful for
// a graceful drain / forcing a reconnect (e.g. after rotating the secret).
func (s *Server) CloseEdges() {
	s.mu.Lock()
	edges := make([]*edgeConn, 0, len(s.edges))
	for e := range s.edges {
		edges = append(edges, e)
	}
	s.mu.Unlock()
	for _, e := range edges {
		_ = e.Conn.Close()
	}
}

func (s *Server) add(e *edgeConn) {
	s.mu.Lock()
	s.edges[e] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) remove(e *edgeConn) {
	s.mu.Lock()
	delete(s.edges, e)
	s.mu.Unlock()
}

func (s *Server) authed(r *http.Request) bool {
	if len(s.secret) == 0 {
		return true // auth disabled (dev)
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), s.secret) == 1
}
