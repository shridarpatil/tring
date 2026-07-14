package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// ── fakes for the agent-initiated outbound dial path (Task 4b) ──────────────

// fakeOutboundSource is a room.ParticipantSource that yields nothing until
// closed — a silent carrier leg, mirroring signaling_test.go's blockingSource
// (unexported in the external test package, so redefined here for the
// white-box tests in this file).
type fakeOutboundSource struct {
	id   room.ParticipantID
	stop chan struct{}
	once sync.Once
}

func (f *fakeOutboundSource) ID() room.ParticipantID { return f.id }
func (f *fakeOutboundSource) ReadRTP() (*rtp.Packet, error) {
	<-f.stop
	return nil, errOutboundSourceClosed
}
func (f *fakeOutboundSource) Close() error {
	f.once.Do(func() { close(f.stop) })
	return nil
}

var errOutboundSourceClosed = errors.New("closed")

// fakeOriginator is a scripted room.Originator + HangupOutbound (the
// internal/fsplug stand-in). calls records every OriginateRequest;
// hangups records every HangupOutbound pid. When release is non-nil,
// Originate blocks on it after recording the call — simulating a carrier ring
// that's still in progress — so a test can act (e.g. the agent leaving) before
// letting Originate resolve.
type fakeOriginator struct {
	leg     *room.Leg
	err     error
	calls   chan room.OriginateRequest
	hangups chan room.ParticipantID
	release chan struct{}
}

func newFakeOriginator(leg *room.Leg) *fakeOriginator {
	return &fakeOriginator{
		leg:     leg,
		calls:   make(chan room.OriginateRequest, 4),
		hangups: make(chan room.ParticipantID, 4),
	}
}

func (f *fakeOriginator) Originate(_ context.Context, req room.OriginateRequest, onProgress func(room.Progress)) (*room.Leg, error) {
	f.calls <- req
	if f.release != nil {
		<-f.release
	}
	if f.err != nil {
		return nil, f.err
	}
	if onProgress != nil {
		onProgress(room.Progress{State: "ringing"})
	}
	return f.leg, nil
}

func (f *fakeOriginator) HangupOutbound(pid room.ParticipantID) error {
	f.hangups <- pid
	return nil
}

// fakeTrunkResolver returns a fixed, already-selected ordered trunk list.
type fakeTrunkResolver struct {
	trunks []trunkselect.Trunk
	err    error
}

func (f fakeTrunkResolver) OutboundTrunks(_ context.Context, _ uuid.UUID) ([]trunkselect.Trunk, error) {
	return f.trunks, f.err
}

func newOutboundServer(t *testing.T, orig Originator, trunks TrunkResolver) *Server {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	return New(Config{SFU: s, Originator: orig, Trunks: trunks})
}

// waitOutboundLeg polls until roomID has a registered outbound leg, returning
// its ParticipantID (or fails the test after a timeout).
func waitOutboundLeg(t *testing.T, s *Server, rid room.RoomID) room.ParticipantID {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		pid, ok := s.outboundLegs[rid]
		s.mu.Unlock()
		if ok {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("outbound leg was never registered for the room")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestHandleAgentDialOriginatesWithSelectedTrunkAndAttachesPort is the core
// Task 4b behavior: an already-joined agent's dial resolves the tenant's
// trunks, originates using the FIRST trunk's E164 as caller-ID and the full
// ordered name list, and plugs the answered leg into the agent's EXISTING
// room (no new room created).
func TestHandleAgentDialOriginatesWithSelectedTrunkAndAttachesPort(t *testing.T) {
	done := make(chan struct{})
	src := &fakeOutboundSource{id: "out-1", stop: make(chan struct{})}
	leg := &room.Leg{
		Source: src,
		Write:  func(*rtp.Packet) error { return nil },
		DTMF:   make(chan string),
		Done:   done,
	}
	orig := newFakeOriginator(leg)
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{
		{E164: "+911600000000", Name: "tata"},
		{E164: "+911600000001", Name: "airtel"},
	}}
	s := newOutboundServer(t, orig, trunks)

	rid := room.RoomID("t/call")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	tenant := uuid.New()

	finished := make(chan struct{})
	go func() {
		s.handleAgentDial(rid, tenant, agent.ID(), "+919900001111")
		close(finished)
	}()

	var req room.OriginateRequest
	select {
	case req = <-orig.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Originate was never called")
	}
	if req.To != "+919900001111" {
		t.Fatalf("To = %q, want the dialed number", req.To)
	}
	if req.CallerID != "+911600000000" {
		t.Fatalf("CallerID = %q, want the FIRST selected trunk's E164", req.CallerID)
	}
	if len(req.Trunks) != 2 || req.Trunks[0] != "tata" || req.Trunks[1] != "airtel" {
		t.Fatalf("Trunks = %v, want [tata airtel]", req.Trunks)
	}

	// The leg's source must be registered as this room's outbound leg AND
	// attached to the room as a port (a port publication exists).
	waitOutboundLeg(t, s, rid)
	if !s.sfu.HasPort(rid) {
		t.Fatal("leg source was not attached to the agent's room as a port")
	}

	// End the call (carrier hangup) so the goroutine completes and the room
	// tears down.
	src.Close()
	close(done)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("handleAgentDial did not return after leg.Done closed")
	}

	// Teardown must have forgotten the outbound leg.
	s.mu.Lock()
	_, stillTracked := s.outboundLegs[rid]
	s.mu.Unlock()
	if stillTracked {
		t.Fatal("outbound leg still tracked after the call ended")
	}
}

// TestHandleAgentDialSendsErrorWhenNoTrunkAvailable: an empty trunk selection
// must not originate anything — the agent gets a msgError instead of a dead
// silent call.
func TestHandleAgentDialSendsErrorWhenNoTrunkAvailable(t *testing.T) {
	orig := newFakeOriginator(nil)
	trunks := fakeTrunkResolver{trunks: nil}
	s := newOutboundServer(t, orig, trunks)

	rid := room.RoomID("t/call-no-trunk")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	s.mu.Lock()
	s.conns[agent.ID()] = &conn{Conn: serverConn, pid: agent.ID()}
	s.mu.Unlock()

	go s.handleAgentDial(rid, uuid.New(), agent.ID(), "+919900001111")

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := wsutil.ReadServerText(clientConn)
	if err != nil {
		t.Fatalf("reading error message: %v", err)
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Type != msgError {
		t.Fatalf("type = %q, want %q", m.Type, msgError)
	}

	select {
	case <-orig.calls:
		t.Fatal("Originate should not have been called with no trunk selected")
	default:
	}
}

// TestHandleAgentDialDisabledWithoutWiringSendsError: with no Originator/Trunks
// configured (outbound disabled), a dial must fail closed with a msgError, not
// hang or panic.
func TestHandleAgentDialDisabledWithoutWiringSendsError(t *testing.T) {
	s := newOutboundServer(t, nil, nil)

	rid := room.RoomID("t/call-disabled")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	s.mu.Lock()
	s.conns[agent.ID()] = &conn{Conn: serverConn, pid: agent.ID()}
	s.mu.Unlock()

	// This path returns synchronously (no Originate call), so it's safe to
	// call directly rather than in a goroutine — but the read below still
	// needs to happen concurrently since conn.send blocks on net.Pipe until
	// the peer reads.
	go s.handleAgentDial(rid, uuid.New(), agent.ID(), "+919900001111")

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := wsutil.ReadServerText(clientConn)
	if err != nil {
		t.Fatalf("reading error message: %v", err)
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Type != msgError {
		t.Fatalf("type = %q, want %q", m.Type, msgError)
	}
}

// TestHangupOutboundIfAbandonedHangsUpWhenAgentLeaves: when the agent who
// placed the outbound call leaves with nobody else on the line, the carrier
// leg must be hung up (routeEvent's EventParticipantLeft → hangupOutboundIfAbandoned),
// not left stranded.
func TestHangupOutboundIfAbandonedHangsUpWhenAgentLeaves(t *testing.T) {
	done := make(chan struct{})
	src := &fakeOutboundSource{id: "out-2", stop: make(chan struct{})}
	leg := &room.Leg{
		Source: src,
		Write:  func(*rtp.Packet) error { return nil },
		DTMF:   make(chan string),
		Done:   done,
	}
	orig := newFakeOriginator(leg)
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}
	s := newOutboundServer(t, orig, trunks)

	rid := room.RoomID("t/call-abandon")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	go s.handleAgentDial(rid, uuid.New(), agent.ID(), "+919900001111")

	select {
	case <-orig.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Originate was never called")
	}
	waitOutboundLeg(t, s, rid)

	// The agent hangs up with nobody else on the call.
	agent.Leave()

	select {
	case pid := <-orig.hangups:
		if pid != src.ID() {
			t.Fatalf("HangupOutbound pid = %q, want %q", pid, src.ID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent leaving alone did not hang up the abandoned outbound leg")
	}

	// Let the goroutine's teardown run to completion.
	src.Close()
	close(done)
}

// TestDialRejectedForVisitor: readLoop's msgDial gate must reject a
// visitor-role socket outright — a visitor also carries a tenant (for
// CDR/token purposes), so without the isAgent gate a visitor could originate a
// billable PSTN call on the tenant's trunk (toll fraud). Originate must never
// be invoked.
func TestDialRejectedForVisitor(t *testing.T) {
	orig := newFakeOriginator(&room.Leg{})
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}
	s := newOutboundServer(t, orig, trunks)

	rid := room.RoomID("t/call-visitor")
	visitor, err := s.rooms.Join(rid, "visitor")
	if err != nil {
		t.Fatalf("join visitor: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	c := &conn{Conn: serverConn, pid: visitor.ID()}

	done := make(chan struct{})
	go func() {
		s.readLoop(c, visitor, uuid.New(), false /* isAgent */, "")
		close(done)
	}()

	dial, _ := json.Marshal(msg{Type: msgDial, To: "+919900001111"})
	if err := wsutil.WriteClientMessage(clientConn, ws.OpText, dial); err != nil {
		t.Fatalf("write dial: %v", err)
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := wsutil.ReadServerText(clientConn)
	if err != nil {
		t.Fatalf("reading error message: %v", err)
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Type != msgError {
		t.Fatalf("type = %q, want %q", m.Type, msgError)
	}

	select {
	case <-orig.calls:
		t.Fatal("Originate must NEVER be called for a visitor-role socket")
	default:
	}

	// End the read loop cleanly.
	bye, _ := json.Marshal(msg{Type: msgBye})
	if err := wsutil.WriteClientMessage(clientConn, ws.OpText, bye); err != nil {
		t.Fatalf("write bye: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not exit after bye")
	}
}

// TestDialAgentLeavesDuringRing: the agent that placed the outbound call
// leaves the room (with nobody else present) WHILE Originate is still
// blocking on the ring. hangupOutboundIfAbandoned no-ops in that window (no
// outboundLegs entry exists yet). When Originate then resolves with an
// answered leg, handleAgentDial's post-Originate recheck must catch the
// abandoned room, HangupOutbound the just-raised carrier, and must NOT attach
// the leg into the room or open a CDR.
func TestDialAgentLeavesDuringRing(t *testing.T) {
	doneLeg := make(chan struct{})
	src := &fakeOutboundSource{id: "out-cancel", stop: make(chan struct{})}
	leg := &room.Leg{
		Source: src,
		Write:  func(*rtp.Packet) error { return nil },
		DTMF:   make(chan string),
		Done:   doneLeg,
	}
	orig := newFakeOriginator(leg)
	orig.release = make(chan struct{})
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}
	s := newOutboundServer(t, orig, trunks)

	rid := room.RoomID("t/call-cancel-ring")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	finished := make(chan struct{})
	go func() {
		s.handleAgentDial(rid, uuid.New(), agent.ID(), "+919900001111")
		close(finished)
	}()

	// Wait until Originate has been invoked (the "ring" is in progress).
	select {
	case <-orig.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Originate was never called")
	}

	// The agent hangs up mid-ring with nobody else on the call.
	agent.Leave()

	// Now let Originate resolve — the callee "answered" too late.
	close(orig.release)

	// The just-raised carrier must be hung up.
	select {
	case pid := <-orig.hangups:
		if pid != src.ID() {
			t.Fatalf("HangupOutbound pid = %q, want %q", pid, src.ID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late-answered leg was not hung up after the agent left mid-ring")
	}

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("handleAgentDial did not return after cancel-while-ringing")
	}

	// The leg must never have been attached to the (now-gone) room, and no
	// outbound leg / CDR should be tracked.
	if s.sfu.HasPort(rid) {
		t.Fatal("leg was attached to a room the agent already left")
	}
	s.mu.Lock()
	_, tracked := s.outboundLegs[rid]
	s.mu.Unlock()
	if tracked {
		t.Fatal("no outbound leg should be tracked after cancel-while-ringing")
	}
}

// TestDialOriginateError: Originate returning an error must send the agent a
// msgError and must not attach anything or register an outbound leg.
func TestDialOriginateError(t *testing.T) {
	orig := newFakeOriginator(nil)
	orig.err = errors.New("carrier rejected")
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}
	s := newOutboundServer(t, orig, trunks)

	rid := room.RoomID("t/call-origfail")
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	s.mu.Lock()
	s.conns[agent.ID()] = &conn{Conn: serverConn, pid: agent.ID()}
	s.mu.Unlock()

	go s.handleAgentDial(rid, uuid.New(), agent.ID(), "+919900001111")

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := wsutil.ReadServerText(clientConn)
	if err != nil {
		t.Fatalf("reading error message: %v", err)
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Type != msgError {
		t.Fatalf("type = %q, want %q", m.Type, msgError)
	}

	if s.sfu.HasPort(rid) {
		t.Fatal("no leg should have been attached after an Originate error")
	}
	s.mu.Lock()
	_, tracked := s.outboundLegs[rid]
	s.mu.Unlock()
	if tracked {
		t.Fatal("no outbound leg should be tracked after an Originate error")
	}
}

// TestDialNormalizesDestination asserts handleAgentDial runs the dialed
// number through dial.India before Originate ever sees it (Fix 1): a bare
// 10-digit Indian mobile number must arrive at Originate as +91-prefixed
// E.164 — matching the ESL/API originate path's behavior, which already
// normalizes. An invalid destination must be rejected with a msgError BEFORE
// any trunk resolution or Originate call.
func TestDialNormalizesDestination(t *testing.T) {
	t.Run("valid 10-digit number is normalized to E.164", func(t *testing.T) {
		done := make(chan struct{})
		src := &fakeOutboundSource{id: "out-norm", stop: make(chan struct{})}
		leg := &room.Leg{
			Source: src,
			Write:  func(*rtp.Packet) error { return nil },
			DTMF:   make(chan string),
			Done:   done,
		}
		orig := newFakeOriginator(leg)
		trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}
		s := newOutboundServer(t, orig, trunks)

		rid := room.RoomID("t/call-normalize")
		agent, err := s.rooms.Join(rid, "agent")
		if err != nil {
			t.Fatalf("join agent: %v", err)
		}

		finished := make(chan struct{})
		go func() {
			s.handleAgentDial(rid, uuid.New(), agent.ID(), "9900001111")
			close(finished)
		}()

		var req room.OriginateRequest
		select {
		case req = <-orig.calls:
		case <-time.After(2 * time.Second):
			t.Fatal("Originate was never called")
		}
		if req.To != "+919900001111" {
			t.Fatalf("To = %q, want normalized E.164 +919900001111", req.To)
		}

		src.Close()
		close(done)
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("handleAgentDial did not return after leg.Done closed")
		}
	})

	t.Run("invalid number errors without dialing", func(t *testing.T) {
		orig := newFakeOriginator(nil)
		trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}
		s := newOutboundServer(t, orig, trunks)

		rid := room.RoomID("t/call-badnumber")
		agent, err := s.rooms.Join(rid, "agent")
		if err != nil {
			t.Fatalf("join agent: %v", err)
		}

		serverConn, clientConn := net.Pipe()
		t.Cleanup(func() { _ = clientConn.Close() })
		s.mu.Lock()
		s.conns[agent.ID()] = &conn{Conn: serverConn, pid: agent.ID()}
		s.mu.Unlock()

		go s.handleAgentDial(rid, uuid.New(), agent.ID(), "12345")

		_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		data, err := wsutil.ReadServerText(clientConn)
		if err != nil {
			t.Fatalf("reading error message: %v", err)
		}
		var m msg
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != msgError {
			t.Fatalf("type = %q, want %q", m.Type, msgError)
		}

		select {
		case <-orig.calls:
			t.Fatal("Originate should not have been called for an invalid destination number")
		default:
		}
	})
}

// NOTE: an AttachPort-failure test (Fix scope item "TestDialAttachPortError")
// was left out — sfu.AttachPort/InjectSource has no reachable failure seam
// from this package's fakes (webrtc.NewTrackLocalStaticRTP essentially never
// errors for a valid Opus capability), so forcing that branch would require
// adding a fault-injection hook to internal/media/sfu itself, out of scope for
// this handler-focused fix. The existing failed-attach branch (hangup +
// sendError, see handleAgentDial) is exercised by code inspection only.

// ── Task 4d: handleAgentDial must open its CDR via StartedOutbound ─────────

// outboundCallRecord captures one StartedOutbound invocation's arguments.
type outboundCallRecord struct {
	roomID   room.RoomID
	from, to string
	pid      room.ParticipantID
}

// fakeOutboundCallLog is a minimal CallLog fake, local to this white-box test
// file (signaling_test.go's fakeCallLog lives in the external `signaling_test`
// package and isn't reachable from here). It records which method fired —
// Started vs StartedOutbound — so a future revert to Started (which would
// silently mislabel an agent-initiated outbound call as inbound, the Task 4d
// bug this exercise pins down) fails a test instead of shipping unnoticed.
type fakeOutboundCallLog struct {
	mu              sync.Mutex
	startedCalls    int
	startedOutbound []outboundCallRecord
}

func (f *fakeOutboundCallLog) Started(_ context.Context, _ uuid.UUID, _, _, _, _, _ string, _ room.ParticipantID) room.TrackSink {
	f.mu.Lock()
	f.startedCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeOutboundCallLog) StartedOutbound(_ context.Context, _ uuid.UUID, roomID, from, to, _ string, pid room.ParticipantID) room.TrackSink {
	f.mu.Lock()
	f.startedOutbound = append(f.startedOutbound, outboundCallRecord{roomID: room.RoomID(roomID), from: from, to: to, pid: pid})
	f.mu.Unlock()
	return nil
}

func (f *fakeOutboundCallLog) Answered(string, room.ParticipantID, string) room.TrackSink { return nil }
func (f *fakeOutboundCallLog) RingStart(context.Context, string, string, string)          {}
func (f *fakeOutboundCallLog) RingStop(string)                                            {}
func (f *fakeOutboundCallLog) Transferred(string, string, string, string)                 {}
func (f *fakeOutboundCallLog) Journey(string, []ivr.Step)                                 {}
func (f *fakeOutboundCallLog) Ended(string, string)                                       {}

func (f *fakeOutboundCallLog) snapshot() (startedCalls int, outbound []outboundCallRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startedCalls, append([]outboundCallRecord(nil), f.startedOutbound...)
}

// TestHandleAgentDialOpensOutboundCDRViaStartedOutbound is the end-to-end
// wiring guard: with Config.Calls set, a successful handleAgentDial must open
// the CDR through StartedOutbound — never Started — carrying the room the
// agent dialed from, the selected trunk's caller-ID as "from", the dialed
// number as "to", and the carrier leg's participant id. Before this test,
// Config.Calls was never set in this file's tests, so the CDR call site
// (outbound.go) was entirely untested here.
func TestHandleAgentDialOpensOutboundCDRViaStartedOutbound(t *testing.T) {
	done := make(chan struct{})
	src := &fakeOutboundSource{id: "out-cdr", stop: make(chan struct{})}
	leg := &room.Leg{
		Source: src,
		Write:  func(*rtp.Packet) error { return nil },
		DTMF:   make(chan string),
		Done:   done,
	}
	orig := newFakeOriginator(leg)
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+911600000000", Name: "tata"}}}

	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	cl := &fakeOutboundCallLog{}
	srv := New(Config{SFU: s, Originator: orig, Trunks: trunks, Calls: cl})

	rid := room.RoomID("t/call-cdr")
	agent, err := srv.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatalf("join agent: %v", err)
	}
	tenant := uuid.New()

	finished := make(chan struct{})
	go func() {
		srv.handleAgentDial(rid, tenant, agent.ID(), "+919900001111")
		close(finished)
	}()

	select {
	case <-orig.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Originate was never called")
	}
	waitOutboundLeg(t, srv, rid)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, outbound := cl.snapshot(); len(outbound) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("StartedOutbound was never invoked")
		}
		time.Sleep(5 * time.Millisecond)
	}

	startedCalls, outbound := cl.snapshot()
	if startedCalls != 0 {
		t.Fatalf("Started was called %d times, want 0 (outbound calls must use StartedOutbound, not Started)", startedCalls)
	}
	if len(outbound) != 1 {
		t.Fatalf("StartedOutbound called %d times, want 1", len(outbound))
	}
	rec := outbound[0]
	if rec.roomID != rid {
		t.Fatalf("StartedOutbound roomID = %q, want %q", rec.roomID, rid)
	}
	if rec.from != "+911600000000" {
		t.Fatalf("StartedOutbound from = %q, want the selected trunk's caller-id", rec.from)
	}
	if rec.to != "+919900001111" {
		t.Fatalf("StartedOutbound to = %q, want the dialed number", rec.to)
	}
	if rec.pid != src.ID() {
		t.Fatalf("StartedOutbound callerPID = %q, want the carrier leg's pid %q", rec.pid, src.ID())
	}

	// Let the call end so the goroutine and room teardown complete cleanly.
	src.Close()
	close(done)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("handleAgentDial did not return after leg.Done closed")
	}
}
