package signaling

import (
	"testing"

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
