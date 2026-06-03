package cache

import (
	"context"
	"testing"
	"time"
)

// A nil *Cache is the "caching disabled" sentinel used throughout the app
// (empty TRING_REDIS_ADDR, or Redis unreachable at startup). Every method
// must be safe on it: reads miss, writes/deletes are no-ops, none panic.
func TestNilCacheIsSafeNoOp(t *testing.T) {
	var c *Cache // nil
	ctx := context.Background()

	if _, ok := c.Get(ctx, "k"); ok {
		t.Error("Get on nil cache returned ok=true, want miss")
	}
	c.Set(ctx, "k", []byte("v"), time.Minute) // must not panic
	if err := c.Delete(ctx, "k"); err != nil {
		t.Errorf("Delete on nil cache = %v, want nil", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close on nil cache = %v, want nil", err)
	}
}

// An empty address means "no Redis configured" and must yield a nil cache
// (not an error), so callers can treat the result uniformly.
func TestNewEmptyAddrDisablesCache(t *testing.T) {
	c, err := New("")
	if err != nil {
		t.Fatalf("New(\"\") error = %v, want nil", err)
	}
	if c != nil {
		t.Errorf("New(\"\") = %v, want nil cache", c)
	}
}
