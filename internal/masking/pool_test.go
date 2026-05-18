package masking

import (
	"errors"
	"testing"
	"time"
)

func TestAssignReturnsFirstAvailableDID(t *testing.T) {
	p := NewPool([]string{"1001", "1002", "1003"})
	s, err := p.Assign("alice", "bob")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if s.MaskedDID != "1001" {
		t.Errorf("first assign got %q, want 1001", s.MaskedDID)
	}
}

// Two-way matching is the core domain invariant: re-Assign with parties
// swapped MUST return the same session (otherwise the same conversation
// would burn a second DID).
func TestAssignIsSymmetric(t *testing.T) {
	p := NewPool([]string{"1001", "1002"})
	first, _ := p.Assign("alice", "bob")
	again, _ := p.Assign("bob", "alice")
	if first.MaskedDID != again.MaskedDID {
		t.Errorf("symmetric re-assign got new DID %q (expected %q)", again.MaskedDID, first.MaskedDID)
	}
	if p.ActiveSessions() != 1 {
		t.Errorf("ActiveSessions = %d, want 1", p.ActiveSessions())
	}
}

func TestAssignExhaustsPool(t *testing.T) {
	p := NewPool([]string{"1001"})
	if _, err := p.Assign("a", "b"); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	_, err := p.Assign("c", "d")
	if !errors.Is(err, ErrNoAvailableDID) {
		t.Errorf("second assign err = %v, want ErrNoAvailableDID", err)
	}
}

func TestResolveBothDirections(t *testing.T) {
	p := NewPool([]string{"1001"})
	s, _ := p.Assign("alice", "bob")

	if got, err := p.Resolve(s.MaskedDID, "alice"); err != nil || got != "bob" {
		t.Errorf("Resolve(alice) = (%q, %v), want (bob, nil)", got, err)
	}
	if got, err := p.Resolve(s.MaskedDID, "bob"); err != nil || got != "alice" {
		t.Errorf("Resolve(bob) = (%q, %v), want (alice, nil)", got, err)
	}
}

func TestResolveStrangerRejected(t *testing.T) {
	p := NewPool([]string{"1001"})
	s, _ := p.Assign("alice", "bob")
	if _, err := p.Resolve(s.MaskedDID, "charlie"); err == nil {
		t.Error("Resolve from stranger should fail, got nil err")
	}
}

func TestReleaseFreesTheDID(t *testing.T) {
	p := NewPool([]string{"1001"})
	s, _ := p.Assign("alice", "bob")
	p.Release(s.MaskedDID)
	if p.ActiveSessions() != 0 {
		t.Errorf("after Release, active = %d, want 0", p.ActiveSessions())
	}
	// The DID is now reusable for a different pair.
	s2, err := p.Assign("charlie", "dave")
	if err != nil || s2.MaskedDID != "1001" {
		t.Errorf("re-assign after release: did=%q err=%v", s2.MaskedDID, err)
	}
}

func TestLookupExpiredSessionMisses(t *testing.T) {
	p := NewPool([]string{"1001"})
	s, _ := p.Assign("alice", "bob")
	// Backdate expiry to simulate the 2-hour timeout having elapsed.
	s.ExpiresAt = time.Now().Add(-time.Minute)

	if _, err := p.Lookup(s.MaskedDID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expired Lookup err = %v, want ErrSessionNotFound", err)
	}
}
