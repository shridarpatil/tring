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
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/routing"
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

// waitSaw polls until a frame of the given type has been written (the transfer
// runs in a goroutine, so the write may not have landed yet), or times out.
func waitSaw(t *testing.T, f *fakeConn, typ string) bool {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f.saw(typ) {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// A committed blind transfer must terminate the initiating agent's leg with a
// terminal `ended` (not just `transfer-ok`). Without it the agent SDK treats the
// socket close as an unexpected drop and reconnects — rejoining the room (resetting
// the call timer) and prematurely resolving the transfer ring so the real target
// never receives the handed-off call.
func TestTransferEndsInitiatingAgentLeg(t *testing.T) {
	s := newHandoffServer(t)
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/xfer")

	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	// A reachable target so the transfer clears pre-flight and commits.
	target := &fakeConn{}
	s.mu.Lock()
	s.agents[tenant.String()] = map[string]*conn{"bob": target.conn("bob-pid")}
	s.mu.Unlock()

	fc := &fakeConn{}
	go s.handleTransfer(fc.conn(agent.ID()), agent, tenant, "alice", msg{Type: msgTransfer, ToSip: "bob"})

	if !waitSaw(t, fc, msgEnded) {
		t.Fatal("initiating agent was not sent a terminal `ended`; its SDK will reconnect and break the transfer")
	}

	// Unblock the ringing goroutine: the caller hangs up → transfer ctx cancelled.
	caller.Leave()
}

// hasParticipant reports whether pid is a current member of the room.
func hasParticipant(r *room.Room, pid room.ParticipantID) bool {
	for _, p := range r.Participants() {
		if p.ID() == pid {
			return true
		}
	}
	return false
}

// waitGone blocks until pid has been removed from the room (or times out).
func waitGone(t *testing.T, s *Server, rid room.RoomID, pid room.ParticipantID) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if r, ok := s.rooms.Get(rid); !ok || !hasParticipant(r, pid) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("participant was not removed in time")
}

// A blind transfer of an IVR-routed call must NOT resume the paused IVR handoff.
// The call reached the agent via the IVR (AwaitHandoffEnd registered a handoff); if
// the agent's departure is misread as a handoff-end, routeEvent resumes the IVR,
// which walks the flow to completion and tears the caller down (both legs drop).
// The transfer must void the handoff so the room stays alive for the transfer ring.
func TestTransferVoidsPendingHandoff(t *testing.T) {
	s := newHandoffServer(t)
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/v-ivr")

	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	s.setCaller(rid, caller.ID())

	// The IVR is paused in a handoff-wait after routing this call to the agent.
	resume := make(chan struct{})
	target := &fakeConn{}
	s.mu.Lock()
	s.handoffs[rid] = &handoff{caller: caller.ID(), resume: resume}
	s.agents[tenant.String()] = map[string]*conn{"bob": target.conn("bob-pid")}
	s.mu.Unlock()

	fc := &fakeConn{}
	go s.handleTransfer(fc.conn(agent.ID()), agent, tenant, "alice", msg{Type: msgTransfer, ToSip: "bob"})

	waitGone(t, s, rid, agent.ID()) // the initiating agent is dropped (true blind)

	// The paused IVR must NOT have been resumed (resuming ends the flow → caller
	// torn down), and the caller must still be on the call for the transfer ring.
	select {
	case <-resume:
		t.Fatal("transfer resumed the paused IVR handoff; the caller will be torn down")
	default:
	}
	if r, ok := s.rooms.Get(rid); !ok || !hasParticipant(r, caller.ID()) {
		t.Fatal("caller was removed during the transfer")
	}

	caller.Leave() // unblock the ring goroutine
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
	if r, ok := s.rooms.Get(rid); !ok || len(r.Participants()) != 2 {
		t.Fatalf("agent should not be stranded after a rejected transfer; ok=%v room=%v", ok, r)
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

// failXferMsg counts how many times the blind-transfer no-answer prompt is
// resolved — i.e. how many failed transfers reached playTransferFailMessage. It
// returns "" so the play itself is a no-op; we only assert the fail path RAN.
type failXferMsg struct {
	mu    sync.Mutex
	calls int
}

func (r *failXferMsg) TenantTransferFailedAudio(context.Context, uuid.UUID) (string, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return "", nil
}

func (r *failXferMsg) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// shortTeams resolves a team to fixed members with a 1s ring/wait, so a
// never-answered transfer target times out fast (a to_sip target would use the
// 30s router default).
type shortTeams struct{ members map[string][]string }

func (s shortTeams) TeamSIPs(_ context.Context, _ uuid.UUID, team string) ([]string, error) {
	return s.members[team], nil
}

func (s shortTeams) TeamConfig(_ context.Context, _ uuid.UUID, team string) (routing.TeamConfig, bool, error) {
	if _, ok := s.members[team]; !ok {
		return routing.TeamConfig{}, false, nil
	}
	return routing.TeamConfig{Name: team, RingSeconds: 1, MaxWait: 1}, true, nil
}

// A blind transfer whose target never answers must, WITH the caller still on the
// line, resolve + play the tenant's no-answer prompt before hanging the caller up.
// This is the branch the "message not played" report is about. It must run every
// time a transfer fails while the caller is present — the real-world "2nd transfer"
// failure was the caller having already dropped during a SILENT ring (fixed by the
// hold-music fallback), NOT this branch being skipped. Guards that the fail path
// itself is intact.
func TestFailedTransferPlaysNoAnswerMessage(t *testing.T) {
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	xm := &failXferMsg{}
	s := New(Config{
		SFU:             sf,
		Teams:           shortTeams{members: map[string][]string{"sales": {"carol"}}},
		TransferMessage: xm,
	})
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/call")

	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatalf("join caller: %v", err)
	}
	s.setCaller(rid, caller.ID())
	agent, err := s.rooms.Join(rid, "agent") // the transferring agent, already on the call
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	// carol is reachable at pre-flight (registered) but never joins → the ring times out.
	target := &fakeConn{}
	s.mu.Lock()
	s.agents[tenant.String()] = map[string]*conn{"carol": target.conn("carol-pid")}
	s.mu.Unlock()

	fc := &fakeConn{}
	done := make(chan struct{})
	go func() {
		s.handleTransfer(fc.conn(agent.ID()), agent, tenant, "agent", msg{Type: msgTransfer, ToTeam: "sales"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleTransfer did not return (the ring never timed out)")
	}

	if got := xm.count(); got != 1 {
		t.Fatalf("no-answer message resolved %d times, want 1 (a failed transfer did not play its message)", got)
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

// A phone caller is the room's port, not a participant, so the transferring
// agent leaving empties the room. That must not cancel the ring or hang up the
// phone leg: the caller waits for the transfer target.
func TestPhoneTransferKeepsCallerWhileRinging(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/pstn-guard")
	plug := &hangupPlug{hangups: make(chan room.CallID, 1)}
	pstnRoom(s, plug, rid, "call-guard")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	ctx, ok := s.beginTransfer(rid)
	if !ok {
		t.Fatal("beginTransfer should succeed")
	}
	agent.Leave()

	if ctx.Err() != nil || !s.transferActive(rid) {
		t.Fatal("the transfer ring was cancelled when the transferring agent left")
	}
	select {
	case id := <-plug.hangups:
		t.Fatalf("phone leg %q was hung up during the transfer ring", id)
	default:
	}
}

// A phone caller who hangs up while the transfer rings stops the ring.
func TestPhoneCallerHangupEndsTransferRing(t *testing.T) {
	s := newHandoffServer(t)
	rid := room.RoomID("t/pstn-guard-2")
	pstnRoom(s, &hangupPlug{hangups: make(chan room.CallID, 1)}, rid, "call-guard-2")
	ctx, ok := s.beginTransfer(rid)
	if !ok {
		t.Fatal("beginTransfer should succeed")
	}

	s.endPstnRoom(rid)

	if ctx.Err() == nil || s.transferActive(rid) {
		t.Fatal("the transfer ring kept going after the phone caller hung up")
	}
}

// A blind transfer of a phone call that nobody answers plays the no-answer
// message and then hangs up the phone leg (endIfAlone can't: the caller is a port).
func TestFailedPhoneTransferHangsUpCaller(t *testing.T) {
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	xm := &failXferMsg{}
	s := New(Config{
		SFU:             sf,
		Teams:           shortTeams{members: map[string][]string{"sales": {"carol"}}},
		TransferMessage: xm,
	})
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/pstn-fail")
	plug := &hangupPlug{hangups: make(chan room.CallID, 1)}
	pstnRoom(s, plug, rid, "call-fail")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	target := &fakeConn{}
	s.mu.Lock()
	s.agents[tenant.String()] = map[string]*conn{"carol": target.conn("carol-pid")}
	s.mu.Unlock()

	fc := &fakeConn{}
	done := make(chan struct{})
	go func() {
		s.handleTransfer(fc.conn(agent.ID()), agent, tenant, "agent", msg{Type: msgTransfer, ToTeam: "sales"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleTransfer did not return (the ring never timed out)")
	}

	if got := xm.count(); got != 1 {
		t.Fatalf("no-answer message resolved %d times, want 1", got)
	}
	select {
	case id := <-plug.hangups:
		if id != "call-fail" {
			t.Fatalf("hung up %q, want call-fail", id)
		}
	case <-time.After(time.Second):
		t.Fatal("the phone caller was not hung up after the failed transfer")
	}
}
