package orchestrator

import (
	"context"
	"testing"
)

// Normal-tier waiters are served strictly FIFO: the first to enqueue is the
// head until it leaves.
func TestCallQueueNormalFIFO(t *testing.T) {
	ctx := context.Background()
	q := newMemQueue()
	a := q.enqueue(ctx, "sales", false)
	b := q.enqueue(ctx, "sales", false)
	if !q.isHead(ctx, "sales", a) {
		t.Fatal("first enqueued should be head")
	}
	if q.isHead(ctx, "sales", b) {
		t.Fatal("second should not be head while first waits")
	}
	q.remove(ctx, "sales", a)
	if !q.isHead(ctx, "sales", b) {
		t.Fatal("second should become head after first leaves")
	}
}

// The priority tier drains before the normal tier regardless of arrival order:
// a later priority caller jumps ahead of an earlier normal caller.
func TestCallQueuePriorityBeatsNormal(t *testing.T) {
	ctx := context.Background()
	q := newMemQueue()
	n := q.enqueue(ctx, "sales", false) // normal, arrived first
	p := q.enqueue(ctx, "sales", true)  // priority, arrived second
	if !q.isHead(ctx, "sales", p) {
		t.Fatal("priority caller should be head even though it arrived later")
	}
	if q.isHead(ctx, "sales", n) {
		t.Fatal("normal caller should wait behind priority")
	}
	q.remove(ctx, "sales", p)
	if !q.isHead(ctx, "sales", n) {
		t.Fatal("normal caller should be head once priority drains")
	}
}

// remove is idempotent (safe to defer) and queues are isolated per team key.
func TestCallQueueRemoveIdempotentAndIsolated(t *testing.T) {
	ctx := context.Background()
	q := newMemQueue()
	a := q.enqueue(ctx, "sales", false)
	b := q.enqueue(ctx, "support", false)
	q.remove(ctx, "sales", a)
	q.remove(ctx, "sales", a) // double remove — must not panic or affect others
	if !q.isHead(ctx, "support", b) {
		t.Fatal("support queue should be unaffected by sales removals")
	}
	if q.depth(ctx, "support") != 1 || q.depth(ctx, "sales") != 0 {
		t.Fatalf("depth: support=%d sales=%d, want 1/0", q.depth(ctx, "support"), q.depth(ctx, "sales"))
	}
}
