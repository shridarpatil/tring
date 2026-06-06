package orchestrator

import (
	"sync"
	"time"
)

// reservations holds short-lived "this agent is being rung right now" markers.
// An agent's presence only flips to on_call when they ANSWER (the gateway's
// enterCall fires on bridge-connect), not while ringing — so without this, two
// callers that dequeue at nearly the same moment both see the same idle agent as
// available and both ring it (the second never queues). When a caller commits to
// ring an agent we reserve it for the ring window; concurrent callers treat a
// reserved agent as unavailable and queue instead. Keyed by "tenant/sip", each
// entry auto-expires (a safety net if an explicit release is ever missed).
type reservations struct {
	mu sync.Mutex
	m  map[string]time.Time // tenant/sip -> expiry
}

func newReservations() *reservations { return &reservations{m: map[string]time.Time{}} }

func (r *reservations) reserve(key string, ttl time.Duration) {
	r.mu.Lock()
	r.m[key] = time.Now().Add(ttl)
	r.mu.Unlock()
}

func (r *reservations) release(key string) {
	r.mu.Lock()
	delete(r.m, key)
	r.mu.Unlock()
}

func (r *reservations) reserved(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.m[key]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(r.m, key)
		return false
	}
	return true
}

// callQueue is the in-process, per-team waiting line backing the Transfer
// node's queue overflow. Each team has two FIFO tiers — priority and normal —
// drained priority-first: who is calling decides the tier (Stage 3), both
// strictly FIFO. Stage 2 uses the normal tier only.
//
// Scope is one orchestrator per FreeSWITCH (same as the masking pool), so an
// in-memory registry is the right fit; a restart drops waiting callers, which
// is acceptable and documented. A waiter holds a monotonic ticket id; it is at
// the head of line when it is the front of the priority tier (if any priority
// waiters exist) or otherwise the front of the normal tier.
type callQueue struct {
	mu       sync.Mutex
	seq      int64
	priority map[string][]int64
	normal   map[string][]int64
}

func newCallQueue() *callQueue {
	return &callQueue{priority: map[string][]int64{}, normal: map[string][]int64{}}
}

// depth returns how many callers are currently waiting for a team (both tiers).
func (c *callQueue) depth(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.priority[key]) + len(c.normal[key])
}

// enqueue appends a waiter to the team's priority or normal tier and returns
// its ticket id.
func (c *callQueue) enqueue(key string, priority bool) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := c.seq
	if priority {
		c.priority[key] = append(c.priority[key], id)
	} else {
		c.normal[key] = append(c.normal[key], id)
	}
	return id
}

// isHead reports whether id is first in line for the team: the front of the
// priority tier when any priority waiters exist, else the front of the normal
// tier. The head is the only waiter allowed to claim a freed agent, which keeps
// the dispatch order strictly priority-then-FIFO.
func (c *callQueue) isHead(key string, id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.priority[key]; len(p) > 0 {
		return p[0] == id
	}
	if n := c.normal[key]; len(n) > 0 {
		return n[0] == id
	}
	return false
}

// remove drops a waiter from whichever tier holds it. Idempotent — removing a
// ticket that is no longer queued is a no-op, so callers can defer it safely.
func (c *callQueue) remove(key string, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if q := removeID(c.normal[key], id); q != nil {
		c.normal[key] = q
	} else {
		delete(c.normal, key)
	}
	if q := removeID(c.priority[key], id); q != nil {
		c.priority[key] = q
	} else {
		delete(c.priority, key)
	}
}

// removeID returns q without id, or nil when the result is empty (so the caller
// can drop the map entry). Returns q unchanged when id isn't present.
func removeID(q []int64, id int64) []int64 {
	for i, x := range q {
		if x == id {
			q = append(q[:i], q[i+1:]...)
			break
		}
	}
	if len(q) == 0 {
		return nil
	}
	return q
}
