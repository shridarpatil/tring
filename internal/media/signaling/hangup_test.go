package signaling

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
)

// TestHangupAttributionFirstWins: when a caller hangs up, their SDK sends bye and
// the agent's SDK cascades a bye too — the FIRST (the actual initiator) must win.
func TestHangupAttributionFirstWins(t *testing.T) {
	s := &Server{}
	rid := room.RoomID("t/call")
	caller := room.ParticipantID("caller")
	agent := room.ParticipantID("agent")
	s.setRole(caller, roleVisitor)
	s.setRole(agent, roleAgent)

	s.markHangupInitiator(rid, caller) // caller hung up first
	s.markHangupInitiator(rid, agent)  // agent cascaded

	if got := s.endCause(rid); got != "client" {
		t.Fatalf("cause = %q, want client (the party who hung up first)", got)
	}
	// endCause consumes it — a second resolve is a fresh "system".
	if got := s.endCause(rid); got != "system" {
		t.Fatalf("after consume, cause = %q, want system", got)
	}
}

func TestHangupAttributionAgent(t *testing.T) {
	s := &Server{}
	rid := room.RoomID("t/call")
	agent := room.ParticipantID("agent")
	s.setRole(agent, roleAgent)
	s.markHangupInitiator(rid, agent)
	if got := s.endCause(rid); got != "agent" {
		t.Fatalf("cause = %q, want agent", got)
	}
}

// No party hung up (IVR hangup node / no-answer / flow end / drop) → system.
func TestHangupAttributionSystemDefault(t *testing.T) {
	s := &Server{}
	if got := s.endCause(room.RoomID("t/call")); got != "system" {
		t.Fatalf("cause = %q, want system", got)
	}
}

// A supervisor leaving must never be attributed as the call's hangup.
func TestHangupAttributionIgnoresSupervisor(t *testing.T) {
	s := &Server{}
	rid := room.RoomID("t/call")
	sup := room.ParticipantID("sup")
	s.setRole(sup, roleSupervisor)
	s.markHangupInitiator(rid, sup)
	if got := s.endCause(rid); got != "system" {
		t.Fatalf("supervisor attributed; cause = %q, want system", got)
	}
}

// A server-initiated end (flow end) recorded as "system" must not be overridden
// by the remaining party's removal that follows.
func TestHangupAttributionSystemFirstWins(t *testing.T) {
	s := &Server{}
	rid := room.RoomID("t/call")
	caller := room.ParticipantID("caller")
	s.setRole(caller, roleVisitor)
	s.setHangupBy(rid, "system")
	s.setHangupBy(rid, s.hangupRole(caller))
	if got := s.endCause(rid); got != "system" {
		t.Fatalf("cause = %q, want system", got)
	}
}

// An agent who leaves into a post-call flow no longer decides who ends the call.
func TestHangupAttributionCleared(t *testing.T) {
	s := &Server{}
	rid := room.RoomID("t/call")
	s.setHangupBy(rid, "agent")
	s.clearHangupBy(rid)
	if got := s.endCauseOr(rid, "caller_hangup"); got != "caller_hangup" {
		t.Fatalf("cause = %q, want the fallback", got)
	}
}

// A phone leg that ends on its own is the caller hanging up.
func TestPstnCarrierHangupIsCaller(t *testing.T) {
	cl := &fakeOutboundCallLog{}
	s := newOutboundServer(t, nil, nil)
	s.cfg.Calls = cl
	rid := room.RoomID(uuid.NewString() + "/pstn-a")
	pstnRoom(s, &hangupPlug{hangups: make(chan room.CallID, 1)}, rid, "call-a")

	s.endPstnRoom(rid)
	if got := cl.endedCauses(); len(got) == 0 || got[0] != "caller_hangup" {
		t.Fatalf("causes = %v, want caller_hangup first", got)
	}
}

// When WE hang up the phone leg (flow ended / hangup node, nobody serving the
// caller), its Done must not be read as the caller hanging up.
func TestPstnSystemHangupIsSystem(t *testing.T) {
	cl := &fakeOutboundCallLog{}
	s := newOutboundServer(t, nil, nil)
	s.cfg.Calls = cl
	rid := room.RoomID(uuid.NewString() + "/pstn-b")
	plug := &hangupPlug{hangups: make(chan room.CallID, 1)}
	pstnRoom(s, plug, rid, "call-b")

	s.hangupPstnIfAbandoned(rid)
	<-plug.hangups
	s.endPstnRoom(rid) // the leg's Done after our BYE
	if got := cl.endedCauses(); len(got) == 0 || got[0] != "system" {
		t.Fatalf("causes = %v, want system first", got)
	}
}

// A phone caller hanging up on a number the IVR dialed settles as the caller —
// never as the callee whose leg we hang up in response.
func TestIVRDialCallerHangupIsCaller(t *testing.T) {
	leg, src, done := newDialLeg("out-ivr-attr")
	cl := &fakeOutboundCallLog{}
	s := newOutboundServer(t, newFakeOriginator(leg), tataTrunk)
	s.cfg.Calls = cl
	rid := room.RoomID(uuid.NewString() + "/pstn-c")
	pstnRoom(s, &hangupPlug{hangups: make(chan room.CallID, 2)}, rid, "call-c")

	if o, _ := s.dialFromIVR(context.Background(), rid, "9812345678", "", time.Second); o != "completed" {
		t.Fatalf("outcome = %q, want completed", o)
	}
	s.endPstnRoom(rid)
	src.Close()
	close(done)
	if got := cl.endedCauses(); len(got) == 0 || got[0] != "caller_hangup" {
		t.Fatalf("causes = %v, want caller_hangup first", got)
	}
}
