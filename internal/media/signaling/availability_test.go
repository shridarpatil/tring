package signaling

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
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
