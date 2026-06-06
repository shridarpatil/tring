package orchestrator

import "testing"

// Normal-tier waiters are served strictly FIFO: the first to enqueue is the
// head until it leaves.
func TestCallQueueNormalFIFO(t *testing.T) {
	q := newCallQueue()
	a := q.enqueue("sales", false)
	b := q.enqueue("sales", false)
	if !q.isHead("sales", a) {
		t.Fatal("first enqueued should be head")
	}
	if q.isHead("sales", b) {
		t.Fatal("second should not be head while first waits")
	}
	q.remove("sales", a)
	if !q.isHead("sales", b) {
		t.Fatal("second should become head after first leaves")
	}
}

// The priority tier drains before the normal tier regardless of arrival order:
// a later priority caller jumps ahead of an earlier normal caller.
func TestCallQueuePriorityBeatsNormal(t *testing.T) {
	q := newCallQueue()
	n := q.enqueue("sales", false) // normal, arrived first
	p := q.enqueue("sales", true)  // priority, arrived second
	if !q.isHead("sales", p) {
		t.Fatal("priority caller should be head even though it arrived later")
	}
	if q.isHead("sales", n) {
		t.Fatal("normal caller should wait behind priority")
	}
	q.remove("sales", p)
	if !q.isHead("sales", n) {
		t.Fatal("normal caller should be head once priority drains")
	}
}

// remove is idempotent (safe to defer) and queues are isolated per team key.
func TestCallQueueRemoveIdempotentAndIsolated(t *testing.T) {
	q := newCallQueue()
	a := q.enqueue("sales", false)
	b := q.enqueue("support", false)
	q.remove("sales", a)
	q.remove("sales", a) // double remove — must not panic or affect others
	if !q.isHead("support", b) {
		t.Fatal("support queue should be unaffected by sales removals")
	}
}
