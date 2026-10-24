package presence

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
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
	nilStore.FillAvailability(ctx, "t1", "1001", false)
	nilStore.SetAvailability(ctx, "t1", "1001", false)

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

// testRedis returns a cache on a real Redis (TRING_TEST_REDIS or the dev
// default), skipping the test if none is reachable — SETNX is the behaviour
// under test, so an in-memory stand-in wouldn't prove anything.
func testRedis(t *testing.T) *cache.Cache {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	return c
}

// A gateway's cache fill from a DB read that predates an API update must never
// overwrite the API's newer value, whichever reaches Redis first.
func TestAvailabilityStaleFillLoses(t *testing.T) {
	s := New(testRedis(t))
	ctx := context.Background()

	// API write first, then the stale fill: the fill is dropped.
	tenant := "test-" + uuid.NewString()
	t.Cleanup(func() { _ = s.c.Delete(ctx, availabilityKey(tenant, "1001")) })
	s.SetAvailability(ctx, tenant, "1001", false)
	s.FillAvailability(ctx, tenant, "1001", true)
	if v, ok := s.CachedAvailability(ctx, tenant, "1001"); !ok || v {
		t.Fatalf("after set(away) then stale fill: got (%v, %v), want away", v, ok)
	}

	// Stale fill first, then the API write: the write wins.
	tenant2 := "test-" + uuid.NewString()
	t.Cleanup(func() { _ = s.c.Delete(ctx, availabilityKey(tenant2, "1001")) })
	s.FillAvailability(ctx, tenant2, "1001", true)
	s.SetAvailability(ctx, tenant2, "1001", false)
	if v, ok := s.CachedAvailability(ctx, tenant2, "1001"); !ok || v {
		t.Fatalf("after stale fill then set(away): got (%v, %v), want away", v, ok)
	}

	// A plain miss is filled, so the next connect skips the DB.
	tenant3 := "test-" + uuid.NewString()
	t.Cleanup(func() { _ = s.c.Delete(ctx, availabilityKey(tenant3, "1001")) })
	s.FillAvailability(ctx, tenant3, "1001", false)
	if v, ok := s.CachedAvailability(ctx, tenant3, "1001"); !ok || v {
		t.Fatalf("fill on a miss: got (%v, %v), want away", v, ok)
	}
}
