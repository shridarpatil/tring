package signaling

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/room"
)

// readMsg reads one server message off the client end of a net.Pipe conn.
func readMsg(t *testing.T, c net.Conn) msg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := wsutil.ReadServerText(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestCallLivePhoneInIVR: a phone call in its IVR has no room participants —
// the caller is an SFU port — and must still count as live for a supervisor.
func TestCallLivePhoneInIVR(t *testing.T) {
	s := newOutboundServer(t, nil, nil)
	rid := room.RoomID(uuid.NewString() + "/pstn-sup")
	if _, live := s.callLive(rid); live {
		t.Fatal("an empty room is live")
	}
	src := &fakeOutboundSource{id: "phone-leg", stop: make(chan struct{})}
	t.Cleanup(func() { src.Close() })
	if err := s.sfu.AttachPort(rid, src, func(*rtp.Packet) error { return nil }); err != nil {
		t.Fatalf("attach port: %v", err)
	}
	if _, live := s.callLive(rid); !live {
		t.Fatal("a phone call in its IVR is not live")
	}
}

// TestEndPstnRoomClosesSupervisors: a phone call that ends in its IVR has no
// participant to leave, so endPstnRoom itself must end its supervisors.
func TestEndPstnRoomClosesSupervisors(t *testing.T) {
	s := newOutboundServer(t, nil, nil)
	rid := room.RoomID(uuid.NewString() + "/pstn-sup-end")
	pstnRoom(s, &hangupPlug{hangups: make(chan room.CallID, 1)}, rid, "call-sup")

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	s.registerSupervisor(rid, "sup-1", &conn{Conn: serverConn, pid: "sup-1"}, "Supervisor A")

	go s.endPstnRoom(rid)
	if m := readMsg(t, clientConn); m.Type != msgEnded {
		t.Fatalf("supervisor got %q, want %q", m.Type, msgEnded)
	}
}

// TestLateAgentJoinsWhisper: an agent who joins after the supervisor switched to
// whisper is told a supervisor is present, in whisper.
func TestLateAgentJoinsWhisper(t *testing.T) {
	s := newOutboundServer(t, nil, nil)
	rid := room.RoomID(uuid.NewString() + "/pstn-sup-late")
	s.registerSupervisor(rid, "sup-1", &conn{pid: "sup-1"}, "Supervisor A")
	s.setSupervisorMode(rid, "sup-1", "whisper")

	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	s.setRole(agent.ID(), roleAgent)
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	s.mu.Lock()
	s.conns[agent.ID()] = &conn{Conn: serverConn, pid: agent.ID()}
	s.mu.Unlock()

	go s.syncSupervisors(rid)
	m := readMsg(t, clientConn)
	if m.Type != msgSupervisor || !m.Present || m.Mode != "whisper" || m.Name != "Supervisor A" {
		t.Fatalf("agent got %+v, want supervisor present in whisper", m)
	}
}
