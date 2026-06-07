package orchestrator

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/cache"
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
	q := newRedisQueue(c)
	key := "test:" + uuid.NewString() // unique so we never collide with live data

	a := q.enqueue(ctx, key, false)
	b := q.enqueue(ctx, key, false)
	if !q.isHead(ctx, key, a) {
		t.Fatal("first normal waiter should be head")
	}
	if q.isHead(ctx, key, b) {
		t.Fatal("second normal waiter should wait")
	}
	if q.depth(ctx, key) != 2 {
		t.Fatalf("depth = %d, want 2", q.depth(ctx, key))
	}

	p := q.enqueue(ctx, key, true) // priority jumps ahead
	if !q.isHead(ctx, key, p) {
		t.Fatal("priority waiter should be head")
	}

	q.remove(ctx, key, p)
	if !q.isHead(ctx, key, a) {
		t.Fatal("first normal should be head once priority drains")
	}
	q.remove(ctx, key, a)
	if !q.isHead(ctx, key, b) {
		t.Fatal("second normal should be head after first leaves")
	}
	q.remove(ctx, key, b)
	if q.depth(ctx, key) != 0 {
		t.Fatalf("depth = %d, want 0 after draining", q.depth(ctx, key))
	}
}

func TestRedisReservations(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	r := newRedisReservations(c)
	key := "test:" + uuid.NewString()

	if r.reserved(ctx, key) {
		t.Fatal("nothing reserved yet")
	}
	r.reserve(ctx, key, time.Minute)
	if !r.reserved(ctx, key) {
		t.Fatal("should be reserved")
	}
	r.release(ctx, key)
	if r.reserved(ctx, key) {
		t.Fatal("should be free after release")
	}

	// Native Redis TTL expiry.
	r.reserve(ctx, key, 150*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if r.reserved(ctx, key) {
		t.Fatal("reservation should expire via Redis TTL")
	}
}
