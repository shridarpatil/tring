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

// An agent the backend already marked Away is told so on connect; an available
// agent gets no message (the SDK assumes available).
func TestSendAvailabilityOnConnect(t *testing.T) {
	tenant := uuid.NewString()
	for _, available := range []bool{false, true} {
		s := &Server{cfg: Config{Availability: fakeAvailability{available}}, log: slog.Default()}
		serverConn, clientConn := net.Pipe()
		go func() {
			s.sendAvailability(&conn{Conn: serverConn}, tenant, "alice")
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
	s.agents["t"] = map[string]*conn{"alice": {}, "bob": {}}
	s.setAway("t", "bob", true)

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

	s.setAway("t", "bob", false)
	if got := direct(context.Background()); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("direct candidates after available = %v, want [bob]", got)
	}
}
