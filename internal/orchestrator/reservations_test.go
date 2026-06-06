package orchestrator

import (
	"testing"
	"time"
)

// A reserved agent reads as reserved until released or its TTL expires — this is
// what makes a second concurrent caller queue instead of double-ringing the
// agent the first caller is already ringing.
func TestReservations(t *testing.T) {
	r := newReservations()

	if r.reserved("t/alice") {
		t.Fatal("nothing reserved yet")
	}
	r.reserve("t/alice", time.Minute)
	if !r.reserved("t/alice") {
		t.Fatal("alice should be reserved")
	}
	if r.reserved("t/bob") {
		t.Fatal("bob was never reserved")
	}

	r.release("t/alice")
	if r.reserved("t/alice") {
		t.Fatal("alice should be free after release")
	}

	// Expired reservations read as free (and self-clean).
	r.reserve("t/carol", -time.Second)
	if r.reserved("t/carol") {
		t.Fatal("an expired reservation must not count as reserved")
	}
}
