package signaling

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/ivrdrive"
)

type fakeAvailability struct{ available bool }

func (f fakeAvailability) AgentAvailable(context.Context, uuid.UUID, string) (bool, error) {
	return f.available, nil
}

// gatedAvailability holds the on-connect lookup until release is closed, so a
// test can act inside the window between registration and the lookup landing.
type gatedAvailability struct {
	release   chan struct{}
	available bool
}

func (g gatedAvailability) AgentAvailable(context.Context, uuid.UUID, string) (bool, error) {
	<-g.release
	return g.available, nil
}

// An agent the backend already marked Away is told so on connect; an available
// agent gets no message (the SDK assumes available).
func TestSendAvailabilityOnConnect(t *testing.T) {
	tenant := uuid.NewString()
	for _, available := range []bool{false, true} {
		serverConn, clientConn := net.Pipe()
		c := &conn{Conn: serverConn}
		s := &Server{
			cfg:    Config{Availability: fakeAvailability{available}},
			agents: map[string]map[string]*conn{tenant: {"alice": c}},
			log:    slog.Default(),
		}
		go func() {
			s.sendAvailability(c, tenant, "alice")
			_ = serverConn.Close()
		}()
		_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if available {
			if _, err := clientConn.Read(make([]byte, 1)); err == nil {
				t.Fatal("available agent was sent a message")
			}
			continue
		}
		m := readMsg(t, clientConn)
		if m.Type != msgAvailability || m.Available == nil || *m.Available {
			t.Fatalf("got %+v, want availability=false", m)
		}
	}
}

// A backend toggle reaches the agent's presence socket, false included.
func TestPushAvailability(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	s := &Server{agents: map[string]map[string]*conn{"t1": {"alice": {Conn: serverConn}}}, log: slog.Default()}

	go s.PushAvailability("t1", "alice", false)
	if m := readMsg(t, clientConn); m.Type != msgAvailability || m.Available == nil || *m.Available {
		t.Fatalf("got %+v, want availability=false", m)
	}
	go s.PushAvailability("t1", "alice", true)
	if m := readMsg(t, clientConn); m.Available == nil || !*m.Available {
		t.Fatalf("got %+v, want availability=true", m)
	}
	s.PushAvailability("t1", "bob", false) // not connected here: no-op
}

// An agent the backend marked Away stays connected but is never a ring
// candidate — for ring-all (the live socket registry) and a direct-agent target
// alike — and becomes one again once marked available.
func TestRingCandidatesSkipAway(t *testing.T) {
	s := newHandoffServer(t)
	bob := &conn{}
	s.agents["t"] = map[string]*conn{"alice": {}, "bob": bob}
	s.setAway(bob, "t", "bob", true)

	ringAll, _, cleanup := s.Prepare(context.Background(), "t/ring-all", ivrdrive.Target{}, true, nil, "", "")
	defer cleanup()
	if got := ringAll(context.Background()); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("ring-all candidates = %v, want [alice]", got)
	}
	direct, _, cleanup2 := s.Prepare(context.Background(), "t/direct", ivrdrive.Target{AgentSIP: "bob"}, false, []string{"bob"}, "", "")
	defer cleanup2()
	if got := direct(context.Background()); len(got) != 0 {
		t.Fatalf("direct candidates for an Away agent = %v, want none", got)
	}

	s.setAway(bob, "t", "bob", false)
	if got := direct(context.Background()); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("direct candidates after available = %v, want [bob]", got)
	}
}

// connectAgent runs alice's presence socket on s and reads past `ready`. The
// returned client end is closed at cleanup, which ends the handler.
func connectAgent(t *testing.T, s *Server, tenant string) net.Conn {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	go s.handleAgentPresence(&conn{Conn: serverConn}, tenant, "alice")
	t.Cleanup(func() { _ = clientConn.Close() })
	if m := readMsg(t, clientConn); m.Type != msgReady {
		t.Fatalf("first message = %+v, want ready", m)
	}
	return clientConn
}

func ringAllCandidates(t *testing.T, s *Server, tenant string) []string {
	t.Helper()
	cands, _, cleanup := s.Prepare(context.Background(), tenant+"/r-"+uuid.NewString(), ivrdrive.Target{}, true, nil, "", "")
	defer cleanup()
	return cands(context.Background())
}

func waitCandidates(t *testing.T, s *Server, tenant string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(ringAllCandidates(t, s, tenant)) != want {
		if time.Now().After(deadline) {
			t.Fatalf("ring-all candidates = %v, want %d", ringAllCandidates(t, s, tenant), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Between registering and the availability lookup landing, a just-connected
// agent is not ringable; once the lookup says available, they are.
func TestConnectNotRingableUntilLookup(t *testing.T) {
	tenant := uuid.NewString()
	gate := gatedAvailability{release: make(chan struct{}), available: true}
	s := newHandoffServer(t)
	s.cfg.Availability = gate
	connectAgent(t, s, tenant)

	if got := ringAllCandidates(t, s, tenant); len(got) != 0 {
		t.Fatalf("candidates before the lookup = %v, want none", got)
	}
	close(gate.release)
	waitCandidates(t, s, tenant, 1)
}

// Without any way to read the flag, a connecting agent falls back to ringable
// instead of staying Away forever.
func TestConnectRingableWithoutResolver(t *testing.T) {
	tenant := uuid.NewString()
	s := newHandoffServer(t)
	connectAgent(t, s, tenant)
	waitCandidates(t, s, tenant, 1)
}

// A push that lands while the on-connect lookup is in flight is newer than what
// the lookup read, so the stale lookup result neither overwrites the Away state
// nor sends the agent a second, contradicting message.
func TestPushDuringLookupWins(t *testing.T) {
	for _, pushed := range []bool{false, true} {
		tenant := uuid.NewString()
		gate := gatedAvailability{release: make(chan struct{}), available: !pushed}
		s := newHandoffServer(t)
		s.cfg.Availability = gate
		client := connectAgent(t, s, tenant)

		go s.PushAvailability(tenant, "alice", pushed)
		if m := readMsg(t, client); m.Type != msgAvailability || m.Available == nil || *m.Available != pushed {
			t.Fatalf("push message = %+v, want availability=%v", m, pushed)
		}
		close(gate.release)

		_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := client.Read(make([]byte, 1)); err == nil {
			t.Fatal("stale lookup sent a message after the push")
		}
		want := 0
		if pushed {
			want = 1
		}
		time.Sleep(20 * time.Millisecond) // let the lookup goroutine finish
		if got := ringAllCandidates(t, s, tenant); len(got) != want {
			t.Fatalf("pushed available=%v: candidates = %v, want %d", pushed, got, want)
		}
	}
}
