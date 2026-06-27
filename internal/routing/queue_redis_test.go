package routing

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
)

// testRedis returns a cache pointing at a real Redis (TRING_TEST_REDIS or the
// dev default), skipping the test if none is reachable. Exercises the PRODUCTION
// queue/reservation backends, not the in-memory fallback.
func testRedis(t *testing.T) *cache.Cache {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := cache.New(addr)
	if err != nil || c == nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	return c
}

func TestRedisQueueFIFOAndPriority(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	q := NewRedisQueue(c)
	key := "test:" + uuid.NewString() // unique so we never collide with live data

	a := q.Enqueue(ctx, key, false)
	b := q.Enqueue(ctx, key, false)
	if !q.IsHead(ctx, key, a) {
		t.Fatal("first normal waiter should be head")
	}
	if q.IsHead(ctx, key, b) {
		t.Fatal("second normal waiter should wait")
	}
	if q.Depth(ctx, key) != 2 {
		t.Fatalf("depth = %d, want 2", q.Depth(ctx, key))
	}

	p := q.Enqueue(ctx, key, true) // priority jumps ahead
	if !q.IsHead(ctx, key, p) {
		t.Fatal("priority waiter should be head")
	}

	q.Remove(ctx, key, p)
	if !q.IsHead(ctx, key, a) {
		t.Fatal("first normal should be head once priority drains")
	}
	q.Remove(ctx, key, a)
	if !q.IsHead(ctx, key, b) {
		t.Fatal("second normal should be head after first leaves")
	}
	q.Remove(ctx, key, b)
	if q.Depth(ctx, key) != 0 {
		t.Fatalf("depth = %d, want 0 after draining", q.Depth(ctx, key))
	}
}

func TestRedisReservations(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	r := NewRedisReservations(c)
	key := "test:" + uuid.NewString()

	if r.Reserved(ctx, key) {
		t.Fatal("nothing reserved yet")
	}
	r.Reserve(ctx, key, time.Minute)
	if !r.Reserved(ctx, key) {
		t.Fatal("should be reserved")
	}
	r.Release(ctx, key)
	if r.Reserved(ctx, key) {
		t.Fatal("should be free after release")
	}

	// Native Redis TTL expiry.
	r.Reserve(ctx, key, 150*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if r.Reserved(ctx, key) {
		t.Fatal("reservation should expire via Redis TTL")
	}
}
