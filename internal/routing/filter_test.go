package routing

import (
	"context"
	"testing"
)

// With presence off (the common no-Redis fallback both planes rely on),
// FilterAvailable passes the candidate list through unchanged — routing never
// drops agents it can't verify. (Presence-on filtering is covered by the
// Redis-gated tests + the presence package.)
func TestFilterAvailablePassthroughWhenPresenceOff(t *testing.T) {
	sips := []string{"a", "b", "c"}
	got := FilterAvailable(context.Background(), nil, NewMemReservations(), "t1", sips)
	if len(got) != len(sips) {
		t.Fatalf("with presence off, got %v, want all of %v", got, sips)
	}
	for i := range sips {
		if got[i] != sips[i] {
			t.Fatalf("order/contents changed: got %v, want %v", got, sips)
		}
	}
}
