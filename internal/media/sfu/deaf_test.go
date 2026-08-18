package sfu

import (
	"testing"

	"github.com/shridarpatil/tring/internal/media/room"
)

// Hold on the pion plane injects the hold music as an ordinary room publication
// and leaves the held party publishing — so without a subscriber-side gate the
// HOLDING agent hears both. Deafening is that gate: a deafened subscriber hears
// nobody, whatever the publishers' audiences say.
func TestPublicationsExceptHonorsDeaf(t *testing.T) {
	s, err := New(Config{IncludeLoopback: true})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	const rid = room.RoomID("t1/room")
	s.putPub(t, rid, "agent")
	s.putPub(t, rid, "customer")
	s.putPub(t, rid, "hold-music")

	// Baseline: nobody is deaf, the agent hears the room.
	if !hasSource(s.publicationsExcept(rid, "agent"), "customer") {
		t.Fatal("baseline: agent should hear the customer")
	}

	s.SetDeaf(rid, "agent", true)
	if hasSource(s.publicationsExcept(rid, "agent"), "customer") {
		t.Fatal("held agent must not hear the customer")
	}
	if hasSource(s.publicationsExcept(rid, "agent"), "hold-music") {
		t.Fatal("held agent must not hear the hold music")
	}
	// Only the deafened subscriber is affected — the held party still hears the
	// music (that's the whole point of hold) and the agent's own publication.
	if !hasSource(s.publicationsExcept(rid, "customer"), "hold-music") {
		t.Fatal("customer must still hear the hold music")
	}
	if !hasSource(s.publicationsExcept(rid, "customer"), "agent") {
		t.Fatal("deafening must not silence the agent's own publication")
	}

	s.SetDeaf(rid, "agent", false)
	if !hasSource(s.publicationsExcept(rid, "agent"), "customer") {
		t.Fatal("resume must restore the agent's hearing")
	}
}

// Deaf beats audience: even a barging supervisor (audience = everyone) is
// inaudible to a held agent.
func TestDeafBeatsAudience(t *testing.T) {
	s, err := New(Config{IncludeLoopback: true})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	const rid = room.RoomID("t2/room")
	s.putPub(t, rid, "agent")
	s.putPub(t, rid, "sup")

	s.SetAudience(rid, "sup", true, nil) // barge
	s.SetDeaf(rid, "agent", true)
	if hasSource(s.publicationsExcept(rid, "agent"), "sup") {
		t.Fatal("a deafened agent must not hear a barging supervisor")
	}
	// The supervisor is not deaf, so it still hears the agent.
	if !hasSource(s.publicationsExcept(rid, "sup"), "agent") {
		t.Fatal("supervisor must still hear the agent")
	}
}

// SetDeaf on a room that doesn't exist (already torn down) is a no-op, not a
// panic — clearHold runs on teardown paths too.
func TestSetDeafUnknownRoom(t *testing.T) {
	s, err := New(Config{IncludeLoopback: true})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	s.SetDeaf("nope/room", "agent", true)
	s.SetDeaf("nope/room", "agent", false)
}
