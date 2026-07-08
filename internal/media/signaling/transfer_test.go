package signaling

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
)

// fakeConn is a net.Conn that captures server-written WebSocket frames so a test
// can assert on what handleTransfer sent the agent, without a real socket.
type fakeConn struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (f *fakeConn) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}
func (f *fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (f *fakeConn) Close() error                     { return nil }
func (f *fakeConn) LocalAddr() net.Addr              { return nil }
func (f *fakeConn) RemoteAddr() net.Addr             { return nil }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// conn wraps the fake in the signaling *conn the handlers write through.
func (f *fakeConn) conn(pid room.ParticipantID) *conn { return &conn{Conn: f, pid: pid} }

// sawFailed reports whether a transfer-failed frame was written.
func (f *fakeConn) sawFailed() bool { return f.saw(msgTransferFailed) }

// readOnly adapts a Reader to the io.ReadWriter wsutil.ReadServerText wants
// (control-frame writes are discarded — the test never needs them).
type readOnly struct{ io.Reader }

func (readOnly) Write(p []byte) (int, error) { return len(p), nil }

func (f *fakeConn) saw(typ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := readOnly{bytes.NewReader(f.buf.Bytes())}
	for {
		data, err := wsutil.ReadServerText(r)
		if err != nil {
			return false
		}
		var m msg
		if json.Unmarshal(data, &m) == nil && m.Type == typ {
			return true
		}
	}
}

// A self-transfer and a both/neither target are rejected without starting a ring.
func TestHandleTransferValidation(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/v")
	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	_ = caller
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	// self-transfer
	fc := &fakeConn{}
	s.handleTransfer(fc.conn(agent.ID()), agent, uuid.New(), "alice", msg{Type: msgTransfer, ToSip: "alice"})
	if !fc.sawFailed() {
		t.Fatal("self-transfer should be rejected")
	}
	if s.transferActive(rid) {
		t.Fatal("no transfer should be active after a rejected request")
	}

	// both targets
	fc2 := &fakeConn{}
	s.handleTransfer(fc2.conn(agent.ID()), agent, uuid.New(), "alice", msg{Type: msgTransfer, ToSip: "bob", ToTeam: "sales"})
	if !fc2.sawFailed() {
		t.Fatal("both-target transfer should be rejected")
	}

	// neither target
	fc3 := &fakeConn{}
	s.handleTransfer(fc3.conn(agent.ID()), agent, uuid.New(), "alice", msg{Type: msgTransfer})
	if !fc3.sawFailed() {
		t.Fatal("empty transfer should be rejected")
	}
	if s.transferActive(rid) {
		t.Fatal("no transfer should be active after rejected requests")
	}
}

// While a transfer is active, an agent leaving must NOT end the lone caller:
// routeEvent's endIfAlone branch must be suppressed so the caller stays on the
// line while the transfer target rings (or the no-answer message plays).
func TestTransferGuardSuppressesAloneTeardown(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/guard")
	if _, err := s.rooms.Join(rid, "caller"); err != nil {
		t.Fatalf("join caller: %v", err)
	}
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	if _, ok := s.beginTransfer(rid); !ok {
		t.Fatal("beginTransfer should succeed")
	}
	agent.Leave() // fires routeEvent EventParticipantLeft synchronously

	// The room must still hold the caller (guard prevented endIfAlone).
	r, ok := s.rooms.Get(rid)
	if !ok || len(r.Participants()) != 1 {
		t.Fatalf("caller should remain during transfer; ok=%v room=%v", ok, r)
	}
}

func TestTransferGuardLifecycle(t *testing.T) {
	s := &Server{transfers: map[room.RoomID]context.CancelFunc{}}
	rid := room.RoomID("t/r")

	if s.transferActive(rid) {
		t.Fatal("no transfer should be active initially")
	}
	ctx, ok := s.beginTransfer(rid)
	if !ok || ctx == nil {
		t.Fatal("beginTransfer should start a transfer")
	}
	if !s.transferActive(rid) {
		t.Fatal("transfer should be active after begin")
	}
	if _, ok := s.beginTransfer(rid); ok {
		t.Fatal("a second beginTransfer must be rejected (double transfer)")
	}
	s.endTransfer(rid)
	if s.transferActive(rid) {
		t.Fatal("transfer should be inactive after end")
	}
	if ctx.Err() == nil {
		t.Fatal("endTransfer must cancel the transfer context")
	}
}
