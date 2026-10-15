package signaling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// hangupPlug is a minimal room.Plug that only records Hangup — the IVR-dial
// tests drive the caller's phone leg directly via s.pstnCalls.
type hangupPlug struct{ hangups chan room.CallID }

func (p *hangupPlug) OnInbound(func(room.InboundCall))      {}
func (p *hangupPlug) Answer(room.CallID) (*room.Leg, error) { return nil, errors.New("unused") }
func (p *hangupPlug) Reject(room.CallID) error              { return nil }
func (p *hangupPlug) Hangup(id room.CallID) error           { p.hangups <- id; return nil }

func newDialLeg(id room.ParticipantID) (*room.Leg, *fakeOutboundSource, chan struct{}) {
	src := &fakeOutboundSource{id: id, stop: make(chan struct{})}
	done := make(chan struct{})
	return &room.Leg{
		Source: src,
		Write:  func(*rtp.Packet) error { return nil },
		DTMF:   make(chan string),
		Done:   done,
	}, src, done
}

var tataTrunk = fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "02265915000", Name: "t_tata"}}}

// pstnRoom registers rid as an inbound phone call (callID) on s, the way
// handleInboundPhone does, so the IVR-dial teardown paths can find it.
func pstnRoom(s *Server, plug *hangupPlug, rid room.RoomID, callID room.CallID) {
	s.pstnGW = plug
	s.mu.Lock()
	s.pstnCalls[rid] = callID
	s.mu.Unlock()
}

func TestIVRDialAnswerAttachesLegAndCompletes(t *testing.T) {
	leg, src, done := newDialLeg("out-ivr-1")
	orig := newFakeOriginator(leg)
	s := newOutboundServer(t, orig, tataTrunk)
	rid := room.RoomID(uuid.NewString() + "/pstn-a")

	outcome, err := s.dialFromIVR(context.Background(), rid, "9812345678", "", time.Second)
	if err != nil || outcome != "completed" {
		t.Fatalf("outcome = %q, err = %v; want completed", outcome, err)
	}
	req := <-orig.calls
	if req.To != "+919812345678" || req.CallerID != "02265915000" || req.Trunks[0] != "t_tata" {
		t.Fatalf("originate = %+v; want normalized To, trunk DID as caller-ID", req)
	}
	if pid := waitOutboundLeg(t, s, rid); pid != src.ID() {
		t.Fatalf("outbound leg = %q, want %q", pid, src.ID())
	}
	if !s.sfu.HasPort(rid) {
		t.Fatal("answered leg was not attached to the room")
	}
	src.Close()
	close(done)
}

func TestIVRDialFailuresReportNoAnswer(t *testing.T) {
	cases := map[string]struct {
		number string
		orig   *fakeOriginator
		trunks fakeTrunkResolver
	}{
		"originate error": {"9812345678", &fakeOriginator{err: errors.New("486 busy"), calls: make(chan room.OriginateRequest, 1)}, tataTrunk},
		"invalid number":  {"12", newFakeOriginator(nil), tataTrunk},
		"no trunk":        {"9812345678", newFakeOriginator(nil), fakeTrunkResolver{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newOutboundServer(t, tc.orig, tc.trunks)
			rid := room.RoomID(uuid.NewString() + "/pstn-b")
			outcome, err := s.dialFromIVR(context.Background(), rid, tc.number, "", time.Second)
			if err != nil || outcome != "no_answer" {
				t.Fatalf("outcome = %q, err = %v; want no_answer", outcome, err)
			}
			s.mu.Lock()
			_, attached := s.outboundLegs[rid]
			s.mu.Unlock()
			if attached {
				t.Fatal("a failed dial must not register an outbound leg")
			}
		})
	}
}

// ctxOriginator rings until its ctx ends — a callee who never answers.
type ctxOriginator struct{ fakeOriginator }

func (c *ctxOriginator) Originate(ctx context.Context, _ room.OriginateRequest, _ func(room.Progress)) (*room.Leg, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestIVRDialRingTimeoutReportsNoAnswer(t *testing.T) {
	s := newOutboundServer(t, &ctxOriginator{}, tataTrunk)
	start := time.Now()
	outcome, _ := s.dialFromIVR(context.Background(), room.RoomID(uuid.NewString()+"/pstn-c"), "9812345678", "", 50*time.Millisecond)
	if outcome != "no_answer" {
		t.Fatalf("outcome = %q, want no_answer", outcome)
	}
	if time.Since(start) > time.Second {
		t.Fatal("ring did not stop at ring timeout")
	}
}

func TestIVRDialCallerGoneBeforeAnswerHangsUpCallee(t *testing.T) {
	leg, src, done := newDialLeg("out-ivr-2")
	orig := newFakeOriginator(leg)
	orig.release = make(chan struct{})
	s := newOutboundServer(t, orig, tataTrunk)
	rid := room.RoomID(uuid.NewString() + "/pstn-d")

	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan string, 1)
	go func() { o, _ := s.dialFromIVR(ctx, rid, "9812345678", "", 5*time.Second); res <- o }()
	<-orig.calls
	cancel()            // the caller hung up while the callee was ringing
	close(orig.release) // …and the callee answers anyway
	select {
	case pid := <-orig.hangups:
		if pid != src.ID() {
			t.Fatalf("hung up %q, want %q", pid, src.ID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callee answering after the caller left was not hung up")
	}
	if o := <-res; o != "no_answer" {
		t.Fatalf("outcome = %q, want no_answer", o)
	}
	src.Close()
	close(done)
}

func TestIVRDialCalleeHangupHangsUpPhoneCaller(t *testing.T) {
	leg, src, done := newDialLeg("out-ivr-3")
	s := newOutboundServer(t, newFakeOriginator(leg), tataTrunk)
	rid := room.RoomID(uuid.NewString() + "/pstn-e")
	plug := &hangupPlug{hangups: make(chan room.CallID, 2)}
	pstnRoom(s, plug, rid, "call-e")

	if o, _ := s.dialFromIVR(context.Background(), rid, "9812345678", "", time.Second); o != "completed" {
		t.Fatalf("outcome = %q, want completed", o)
	}
	// While the callee is on the line the phone caller must stay up, even though
	// the IVR has finished and no web participant is in the room.
	s.hangupPstnIfAbandoned(rid)
	select {
	case <-plug.hangups:
		t.Fatal("caller hung up while the dialed callee was still connected")
	default:
	}

	src.Close()
	close(done) // the callee hangs up
	select {
	case id := <-plug.hangups:
		if id != "call-e" {
			t.Fatalf("hung up %q, want call-e", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callee hanging up did not hang up the phone caller")
	}
}

func TestIVRDialPhoneCallerHangupHangsUpCallee(t *testing.T) {
	leg, src, done := newDialLeg("out-ivr-4")
	orig := newFakeOriginator(leg)
	s := newOutboundServer(t, orig, tataTrunk)
	rid := room.RoomID(uuid.NewString() + "/pstn-f")
	pstnRoom(s, &hangupPlug{hangups: make(chan room.CallID, 2)}, rid, "call-f")

	if o, _ := s.dialFromIVR(context.Background(), rid, "9812345678", "", time.Second); o != "completed" {
		t.Fatalf("outcome = %q, want completed", o)
	}
	s.endPstnRoom(rid) // the phone caller hung up
	select {
	case pid := <-orig.hangups:
		if pid != src.ID() {
			t.Fatalf("hung up %q, want %q", pid, src.ID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("phone caller hanging up did not hang up the dialed callee")
	}
	src.Close()
	close(done)
}

func TestIVRDialPresentsOwnedCallerID(t *testing.T) {
	leg, src, done := newDialLeg("out-ivr-5")
	orig := newFakeOriginator(leg)
	trunks := tataTrunk
	trunks.owned = map[string]bool{"02265915123": true}
	s := newOutboundServer(t, orig, trunks)

	o, _ := s.dialFromIVR(context.Background(), room.RoomID(uuid.NewString()+"/pstn-g"), "9812345678", "02265915123", time.Second)
	if o != "completed" {
		t.Fatalf("outcome = %q, want completed", o)
	}
	if req := <-orig.calls; req.CallerID != "02265915123" {
		t.Fatalf("caller id = %q, want the override", req.CallerID)
	}
	src.Close()
	close(done)
}

func TestIVRDialRefusesUnownedCallerID(t *testing.T) {
	orig := newFakeOriginator(nil)
	s := newOutboundServer(t, orig, tataTrunk) // owns nothing beyond its trunk DID
	o, _ := s.dialFromIVR(context.Background(), room.RoomID(uuid.NewString()+"/pstn-h"), "9812345678", "02299999999", time.Second)
	if o != "no_answer" {
		t.Fatalf("outcome = %q, want no_answer for a caller id the tenant doesn't hold", o)
	}
	select {
	case req := <-orig.calls:
		t.Fatalf("originated with an unowned caller id: %+v", req)
	default:
	}
}
