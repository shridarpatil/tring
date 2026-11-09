package cache

import (
	"context"
	"os"
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
	c, err := New("", "")
	if err != nil {
		t.Fatalf("New(\"\") error = %v, want nil", err)
	}
	if c != nil {
		t.Errorf("New(\"\") = %v, want nil cache", c)
	}
}

// Every Redis client is built from Options, so the password must land in the
// go-redis options (sent as AUTH on connect) and an empty one must mean no auth.
func TestOptionsSetsAddrAndPassword(t *testing.T) {
	o := Options("127.0.0.1:6379", "s3cret-pass")
	if o.Addr != "127.0.0.1:6379" {
		t.Errorf("Addr = %q, want 127.0.0.1:6379", o.Addr)
	}
	if o.Password != "s3cret-pass" {
		t.Error("Password not set from argument")
	}
	if o := Options("127.0.0.1:6379", ""); o.Password != "" {
		t.Error("empty password should yield no auth, got a non-empty Password")
	}
}

// testRedis returns a live cache or skips; TRING_TEST_REDIS is host:port with
// no password.
func testRedis(t *testing.T) *Cache {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		t.Skip("TRING_TEST_REDIS not set")
	}
	c, err := New(addr, "")
	if err != nil || c == nil {
		t.Skipf("redis unavailable: %v", err)
	}
	if err := c.rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis unreachable: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// GetDel backs single-use SSO state/code: the second read must miss.
func TestGetDelSingleUse(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	const k = "test:getdel:single-use"
	c.Set(ctx, k, []byte("v"), time.Minute)

	val, ok, err := c.GetDel(ctx, k)
	if err != nil || !ok || string(val) != "v" {
		t.Fatalf("first GetDel = %q,%v,%v; want v,true,nil", val, ok, err)
	}
	val, ok, err = c.GetDel(ctx, k)
	if err != nil || ok || val != nil {
		t.Fatalf("second GetDel = %q,%v,%v; want nil,false,nil", val, ok, err)
	}
}

// A nil cache must surface an error, not a miss, so single-use callers fail closed.
func TestGetDelNilCache(t *testing.T) {
	var c *Cache
	if _, ok, err := c.GetDel(context.Background(), "k"); err == nil || ok {
		t.Errorf("GetDel on nil cache = ok %v, err %v; want error", ok, err)
	}
}
