package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"
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

	preg    sync.Mutex
	pending map[string]chan Message // command id → response waiter
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
		pending: make(map[string]chan Message),
	}
}

// SetOnEvent swaps the event handler (lets the brain construct the server first,
// then wire a handler that needs the server — e.g. WebIVR, which sends commands).
func (s *Server) SetOnEvent(fn func(Message)) {
	if fn == nil {
		fn = func(Message) {}
	}
	s.onEvent = fn
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
			// A response to an in-flight Request (correlated by id) goes to its
			// waiter; everything else is an unsolicited event for the app.
			if m.ID != "" && s.deliver(m) {
				continue
			}
			s.onEvent(m)
		}
	}
}

// Request sends a command and blocks until the edge responds (correlated by a
// generated id) or ctx is done. Returns ErrNotConnected if no edge is attached.
func (s *Server) Request(ctx context.Context, m Message) (Message, error) {
	m.ID = uuid.NewString()
	ch := make(chan Message, 1)
	s.preg.Lock()
	s.pending[m.ID] = ch
	s.preg.Unlock()
	defer func() {
		s.preg.Lock()
		delete(s.pending, m.ID)
		s.preg.Unlock()
	}()

	if s.Send(m) == 0 {
		return Message{}, ErrNotConnected
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

// deliver routes a correlated response to its waiter; false if none is waiting.
func (s *Server) deliver(m Message) bool {
	s.preg.Lock()
	ch := s.pending[m.ID]
	s.preg.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- m:
	default:
	}
	return true
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
