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
	c, err := cache.New(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD"))
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
	r.Reserve(ctx, key, "owner", time.Minute)
	if !r.Reserved(ctx, key) {
		t.Fatal("should be reserved")
	}
	r.Release(ctx, key, "owner")
	if r.Reserved(ctx, key) {
		t.Fatal("should be free after release")
	}

	// Native Redis TTL expiry.
	r.Reserve(ctx, key, "owner", 150*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if r.Reserved(ctx, key) {
		t.Fatal("reservation should expire via Redis TTL")
	}
}

// A waiter whose gateway died (crash, SIGKILL, drain timeout) or whose Remove
// was lost to a Redis error leaves its ticket in the list. Once its lease
// lapses it must stop blocking the line; otherwise IsHead is false for every
// later caller and nobody is rung even with idle agents.
func TestRedisQueueSkipsDeadHead(t *testing.T) {
	c := testRedis(t)
	ctx := context.Background()
	q := newRedisQueue(c, 300*time.Millisecond)
	key := "test:" + uuid.NewString()

	dead := q.Enqueue(ctx, key, false)
	live := q.Enqueue(ctx, key, false)
	defer q.Remove(ctx, key, live)
	q.stopHeartbeat(dead) // the owning process is gone; nothing renews its lease

	if q.IsHead(ctx, key, live) {
		t.Fatal("live waiter jumped a head whose lease is still valid")
	}
	time.Sleep(time.Second) // dead lease expires; the live one keeps renewing
	if !q.IsHead(ctx, key, live) {
		t.Fatal("dead ticket still blocks the head of line after its lease expired")
	}
	if d := q.Depth(ctx, key); d != 1 {
		t.Fatalf("depth = %d, want 1 once the dead ticket is swept", d)
	}
}

// The rejoined ticket carries its own lease, so the dead-head sweep keeps it
// at the front.
func TestRedisQueueEnqueueFront(t *testing.T) {
	testRejoinFront(t, NewRedisQueue(testRedis(t)), "test:"+uuid.NewString())
}
