package orchestrator

import (
	"context"
	"testing"
	"time"
)

// A reserved agent reads as reserved until released or its TTL expires — this is
// what makes a second concurrent caller queue instead of double-ringing the
// agent the first caller is already ringing. (in-memory backend; the Redis
// backend is a thin wrapper over cache Set/Get/Delete with native TTL.)
func TestReservations(t *testing.T) {
	ctx := context.Background()
	r := newMemReservations()

	if r.reserved(ctx, "t/alice") {
		t.Fatal("nothing reserved yet")
	}
	r.reserve(ctx, "t/alice", time.Minute)
	if !r.reserved(ctx, "t/alice") {
		t.Fatal("alice should be reserved")
	}
	if r.reserved(ctx, "t/bob") {
		t.Fatal("bob was never reserved")
	}

	r.release(ctx, "t/alice")
	if r.reserved(ctx, "t/alice") {
		t.Fatal("alice should be free after release")
	}

	// Expired reservations read as free (and self-clean).
	r.reserve(ctx, "t/carol", -time.Second)
	if r.reserved(ctx, "t/carol") {
		t.Fatal("an expired reservation must not count as reserved")
	}
}
