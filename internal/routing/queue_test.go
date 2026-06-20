package routing

import (
	"context"
	"testing"
	"time"
)

// Normal-tier waiters are served strictly FIFO: the first to enqueue is the
// head until it leaves.
func TestQueueNormalFIFO(t *testing.T) {
	ctx := context.Background()
	q := NewMemQueue()
	a := q.Enqueue(ctx, "sales", false)
	b := q.Enqueue(ctx, "sales", false)
	if !q.IsHead(ctx, "sales", a) {
		t.Fatal("first enqueued should be head")
	}
	if q.IsHead(ctx, "sales", b) {
		t.Fatal("second should not be head while first waits")
	}
	q.Remove(ctx, "sales", a)
	if !q.IsHead(ctx, "sales", b) {
		t.Fatal("second should become head after first leaves")
	}
}

// The priority tier drains before the normal tier regardless of arrival order.
func TestQueuePriorityBeatsNormal(t *testing.T) {
	ctx := context.Background()
	q := NewMemQueue()
	n := q.Enqueue(ctx, "sales", false) // normal, arrived first
	p := q.Enqueue(ctx, "sales", true)  // priority, arrived second
	if !q.IsHead(ctx, "sales", p) {
		t.Fatal("priority caller should be head even though it arrived later")
	}
	if q.IsHead(ctx, "sales", n) {
		t.Fatal("normal caller should wait behind priority")
	}
	q.Remove(ctx, "sales", p)
	if !q.IsHead(ctx, "sales", n) {
		t.Fatal("normal caller should be head once priority drains")
	}
}

// Remove is idempotent (safe to defer) and queues are isolated per team key.
func TestQueueRemoveIdempotentAndIsolated(t *testing.T) {
	ctx := context.Background()
	q := NewMemQueue()
	a := q.Enqueue(ctx, "sales", false)
	b := q.Enqueue(ctx, "support", false)
	q.Remove(ctx, "sales", a)
	q.Remove(ctx, "sales", a) // double remove — must not panic or affect others
	if !q.IsHead(ctx, "support", b) {
		t.Fatal("support queue should be unaffected by sales removals")
	}
	if q.Depth(ctx, "support") != 1 || q.Depth(ctx, "sales") != 0 {
		t.Fatalf("depth: support=%d sales=%d, want 1/0", q.Depth(ctx, "support"), q.Depth(ctx, "sales"))
	}
}

// A reservation holds until released, and an expired one reads as free.
func TestReservations(t *testing.T) {
	ctx := context.Background()
	r := NewMemReservations()
	k := ResKey("t1", "alice")

	if r.Reserved(ctx, k) {
		t.Fatal("fresh key should not be reserved")
	}
	r.Reserve(ctx, k, time.Minute)
	if !r.Reserved(ctx, k) {
		t.Fatal("reserved key should read reserved")
	}
	r.Release(ctx, k)
	if r.Reserved(ctx, k) {
		t.Fatal("released key should read free")
	}

	// Expiry: a zero-TTL reservation is already past when checked.
	r.Reserve(ctx, k, -time.Second)
	if r.Reserved(ctx, k) {
		t.Fatal("expired reservation should read free")
	}
}

func TestKeys(t *testing.T) {
	if QueueKey("t1", "sales") != "t1/sales" {
		t.Fatalf("QueueKey = %q", QueueKey("t1", "sales"))
	}
	if ResKey("t1", "alice") != "t1/alice" {
		t.Fatalf("ResKey = %q", ResKey("t1", "alice"))
	}
}
