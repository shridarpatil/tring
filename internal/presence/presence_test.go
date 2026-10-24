package presence

import (
	"context"
	"testing"
)

// A nil/disabled store must report Enabled()==false and never claim an agent is
// online — callers rely on that to fall back to pre-cutover behaviour (ring all
// agents / show "unknown") instead of treating everyone as offline.
func TestDisabledStoreIsSafe(t *testing.T) {
	ctx := context.Background()

	var nilStore *Store
	if nilStore.Enabled() {
		t.Error("nil *Store should not be Enabled")
	}
	if nilStore.IsOnline(ctx, "t1", "1001") {
		t.Error("nil *Store must not report online")
	}
	// MarkOnline/Offline on a nil store must be no-ops, not panics.
	nilStore.MarkOnline(ctx, "t1", "1001")
	nilStore.Offline(ctx, "t1", "1001")
	// The availability cache misses (so callers read the DB) and never panics.
	if _, ok := nilStore.CachedAvailability(ctx, "t1", "1001"); ok {
		t.Error("nil *Store must miss the availability cache")
	}
	nilStore.CacheAvailability(ctx, "t1", "1001", false)
	nilStore.InvalidateAvailability(ctx, "t1", "1001")

	// Built on a nil cache (Redis disabled) — same contract.
	s := New(nil)
	if s.Enabled() {
		t.Error("store over a nil cache should not be Enabled")
	}
	if s.IsOnline(ctx, "t1", "1001") {
		t.Error("disabled store must not report online")
	}
}

func TestKeyIsTenantScoped(t *testing.T) {
	if key("t1", "1001") == key("t2", "1001") {
		t.Error("presence key must be tenant-scoped (same sip, different tenant)")
	}
	if key("t1", "1001") != "presence:agent:t1:1001" {
		t.Errorf("unexpected key format: %q", key("t1", "1001"))
	}
}
