package routing

import (
	"context"
	"strings"
	"testing"
)

// round_robin rotates the starting member each call so load spreads across the
// team rather than always hammering the first member.
func TestOrderByStrategyRoundRobin(t *testing.T) {
	rr := NewMemRR()
	base := []string{"a", "b", "c"}
	want := [][]string{
		{"a", "b", "c"},
		{"b", "c", "a"},
		{"c", "a", "b"},
		{"a", "b", "c"}, // wraps
	}
	for i, exp := range want {
		got := OrderByStrategy(context.Background(), nil, rr, "t1", "sales", "round_robin", append([]string{}, base...))
		if strings.Join(got, ",") != strings.Join(exp, ",") {
			t.Fatalf("call %d: got %v, want %v", i, got, exp)
		}
	}
}

// sequential (and ring_all / "") keep membership-position order untouched.
func TestOrderByStrategySequentialKeepsOrder(t *testing.T) {
	rr := NewMemRR()
	base := []string{"a", "b", "c"}
	for _, strat := range []string{"sequential", "ring_all", ""} {
		got := OrderByStrategy(context.Background(), nil, rr, "t1", "sales", strat, append([]string{}, base...))
		if strings.Join(got, ",") != "a,b,c" {
			t.Fatalf("strategy %q reordered: got %v", strat, got)
		}
	}
}

// RRCounter returns 0..n-1, advances, and keeps separate keys independent.
func TestRRCounterIndependentKeys(t *testing.T) {
	rr := NewMemRR()
	if got := rr.Next("x", 2); got != 0 {
		t.Fatalf("first x = %d, want 0", got)
	}
	if got := rr.Next("y", 2); got != 0 {
		t.Fatalf("first y = %d, want 0 (independent of x)", got)
	}
	if got := rr.Next("x", 2); got != 1 {
		t.Fatalf("second x = %d, want 1", got)
	}
}
