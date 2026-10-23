package signaling

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// deafSpy records the last deafened state per participant.
type deafSpy struct {
	mu   sync.Mutex
	last map[room.ParticipantID]bool
}

func (d *deafSpy) set(_ room.RoomID, pid room.ParticipantID, deaf bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		d.last = map[room.ParticipantID]bool{}
	}
	d.last[pid] = deaf
}

func (d *deafSpy) get(pid room.ParticipantID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last[pid]
}

// loopClient drives Server.readLoop over an in-memory pipe, standing in for a
// joined /rtc socket. (A real visitor join needs Redis to burn the token's jti,
// so the role gate is exercised directly on the read loop.)
type loopClient struct {
	t    *testing.T
	conn net.Conn
	msgs chan msg
}

func startLoop(t *testing.T, s *Server, rid room.RoomID, isAgent bool) (*loopClient, *room.Participant) {
	t.Helper()
	p, err := s.rooms.Join(rid, "x")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = cli.Close() })
	c := &conn{Conn: srv, pid: p.ID()}
	go s.readLoop(c, p, uuid.New(), isAgent, "alice")

	lc := &loopClient{t: t, conn: cli, msgs: make(chan msg, 16)}
	go func() {
		for {
			data, err := wsutil.ReadServerText(cli)
			if err != nil {
				close(lc.msgs)
				return
			}
			var m msg
			if json.Unmarshal(data, &m) == nil {
				lc.msgs <- m
			}
		}
	}()
	return lc, p
}

func (c *loopClient) send(m msg) {
	c.t.Helper()
	b, _ := json.Marshal(m)
	if err := wsutil.WriteClientMessage(c.conn, ws.OpText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// sync returns once every earlier message has been handled: net.Pipe writes
// block until read, and the read loop only reads the next frame after it has
// finished dispatching the previous one.
func (c *loopClient) sync() {
	c.t.Helper()
	c.send(msg{Type: msgPing})
	c.send(msg{Type: msgPing})
}

func (c *loopClient) expectError(want string) {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatalf("socket closed, want error %q", want)
		}
		if m.Type != msgError || m.Reason != want {
			c.t.Fatalf("got %+v, want error %q", m, want)
		}
	case <-time.After(3 * time.Second):
		c.t.Fatalf("no error %q", want)
	}
}

func (c *loopClient) expectNothing() {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if ok {
			c.t.Fatalf("unexpected message %+v", m)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func controlsServer(t *testing.T) (*Server, *deafSpy) {
	t.Helper()
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu: %v", err)
	}
	s := New(Config{SFU: sf})
	spy := &deafSpy{}
	s.setDeaf = spy.set
	return s, spy
}

func (s *Server) held(rid room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.holds[rid]
	return ok
}

// A visitor must not be able to take the agent's call off hold (which would
// also un-deafen the agent) — only an agent can resume.
func TestVisitorCannotResume(t *testing.T) {
	s, spy := controlsServer(t)
	rid := room.RoomID(uuid.NewString() + "/call")
	agent := room.ParticipantID("agent-1")
	s.holdParty(rid, agent)

	v, _ := startLoop(t, s, rid, false)
	v.send(msg{Type: msgResume})
	v.expectError("only an agent can resume")

	if !s.held(rid) {
		t.Fatal("visitor resume cleared the agent's hold")
	}
	if !spy.get(agent) {
		t.Fatal("visitor resume un-deafened the holding agent")
	}
}

// A visitor must not be able to put the call on hold (hold music + deafening
// itself / the room).
func TestVisitorCannotHold(t *testing.T) {
	s, spy := controlsServer(t)
	rid := room.RoomID(uuid.NewString() + "/call")
	v, p := startLoop(t, s, rid, false)
	v.send(msg{Type: msgHold})
	v.expectError("only an agent can hold")

	if s.held(rid) {
		t.Fatal("visitor hold started hold music")
	}
	if spy.get(p.ID()) {
		t.Fatal("visitor hold deafened a participant")
	}
}

// The agent's own hold/resume still works over the read loop.
func TestAgentHoldResume(t *testing.T) {
	s, spy := controlsServer(t)
	rid := room.RoomID(uuid.NewString() + "/call")
	a, p := startLoop(t, s, rid, true)

	a.send(msg{Type: msgHold})
	a.sync()
	a.expectNothing()
	if !s.held(rid) || !spy.get(p.ID()) {
		t.Fatal("agent hold did not hold + deafen")
	}
	a.send(msg{Type: msgResume})
	a.sync()
	a.expectNothing()
	if s.held(rid) || spy.get(p.ID()) {
		t.Fatal("agent resume did not clear hold + un-deafen")
	}
}

// A visitor's publish/unpublish stays allowed: the guest SDK screen-shares
// (TringGuest.shareScreen → publish{kind:video}). It must not error.
func TestVisitorMayPublishScreen(t *testing.T) {
	s, _ := controlsServer(t)
	rid := room.RoomID(uuid.NewString() + "/call")
	v, _ := startLoop(t, s, rid, false)
	v.send(msg{Type: msgPublish, Kind: "video"})
	v.send(msg{Type: msgUnpublish, Kind: "video"})
	v.sync()
	v.expectNothing()
}

// A second hold while already held must be a no-op — it must NOT un-deafen
// the holder (the old losing branch ran SetDeaf(holder,false) while the first
// hold's music kept playing).
func TestDoubleHoldKeepsHolderDeaf(t *testing.T) {
	s, spy := controlsServer(t)
	rid := room.RoomID(uuid.NewString() + "/call")
	agent := room.ParticipantID("agent-1")

	s.holdParty(rid, agent)
	s.holdParty(rid, agent)
	if !spy.get(agent) {
		t.Fatal("second hold un-deafened the holder while the call is still held")
	}
	if !s.held(rid) {
		t.Fatal("second hold dropped the hold")
	}
	s.resumeParty(rid)
	if spy.get(agent) || s.held(rid) {
		t.Fatal("resume after double hold did not fully release")
	}
}

// Concurrent holds + a resume must always end released (not deaf, not held).
func TestConcurrentHoldThenResume(t *testing.T) {
	s, spy := controlsServer(t)
	rid := room.RoomID(uuid.NewString() + "/call")
	agent := room.ParticipantID("agent-1")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.holdParty(rid, agent) }()
	}
	wg.Wait()
	if !spy.get(agent) || !s.held(rid) {
		t.Fatal("concurrent holds left the call un-held or the holder hearing")
	}
	s.resumeParty(rid)
	if spy.get(agent) || s.held(rid) {
		t.Fatal("resume did not release")
	}
}
