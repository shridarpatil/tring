// Package routing is the transport-agnostic agent-routing brain shared by both
// media planes: the FreeSWITCH/orchestrator path and the pion/gateway path. It
// owns the queue discipline — who waits for a team (Queue), which agents are
// mid-ring (Reservations), which agents are available (FilterAvailable), the
// ring order per team strategy (OrderByStrategy), team config, and queue-depth
// publishing — so that decision logic lives in ONE place instead of drifting
// between two copies. How a call is actually rung and an answer detected is
// transport-specific and stays behind the Effects seam (effects.go); the planes
// differ only there.
package routing

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/cache"
)

// QueueKey scopes a team's waiting line to a tenant; ResKey scopes an agent's
// ring reservation to a tenant. Both planes build keys the same way so a shared
// Redis backend lines up across the FreeSWITCH and pion processes.
func QueueKey(tenantID, team string) string { return tenantID + "/" + team }
func ResKey(tenantID, sip string) string    { return tenantID + "/" + sip }

// ─── Reservations ───────────────────────────────────────────────────

// Reservations marks an agent as "being rung right now". Presence only flips to
// on_call when the agent ANSWERS, so without this two callers dequeuing at once
// both see the same idle agent as free and double-ring it. The Redis backend is
// shared across processes (FreeSWITCH + pion) serving the same agents; the
// in-memory backend is the no-Redis fallback (host dev / unit tests).
type Reservations interface {
	Reserve(ctx context.Context, key string, ttl time.Duration)
	Release(ctx context.Context, key string)
	Reserved(ctx context.Context, key string) bool
}

// memReservations is the in-memory fallback (single process, no Redis).
type memReservations struct {
	mu sync.Mutex
	m  map[string]time.Time // key -> expiry
}

// NewMemReservations returns the in-memory Reservations backend.
func NewMemReservations() Reservations { return &memReservations{m: map[string]time.Time{}} }

func (r *memReservations) Reserve(_ context.Context, key string, ttl time.Duration) {
	r.mu.Lock()
	r.m[key] = time.Now().Add(ttl)
	r.mu.Unlock()
}

func (r *memReservations) Release(_ context.Context, key string) {
	r.mu.Lock()
	delete(r.m, key)
	r.mu.Unlock()
}

func (r *memReservations) Reserved(_ context.Context, key string) bool {
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

// redisReservations is the production backend: a TTL'd Redis key per agent,
// visible to every process sharing the cache. Redis handles expiry.
type redisReservations struct{ c *cache.Cache }

// NewRedisReservations returns the Redis-backed Reservations (shared, durable).
func NewRedisReservations(c *cache.Cache) Reservations { return &redisReservations{c: c} }

func resvRedisKey(key string) string { return "tring:resv:" + key }

func (r *redisReservations) Reserve(ctx context.Context, key string, ttl time.Duration) {
	r.c.Set(ctx, resvRedisKey(key), []byte("1"), ttl)
}
func (r *redisReservations) Release(ctx context.Context, key string) {
	_ = r.c.Delete(ctx, resvRedisKey(key))
}
func (r *redisReservations) Reserved(ctx context.Context, key string) bool {
	_, ok := r.c.Get(ctx, resvRedisKey(key))
	return ok
}

// ─── Queue ──────────────────────────────────────────────────────────

// Queue is a per-team waiting line with two FIFO tiers (priority, normal),
// drained priority-first. A waiter holds a unique ticket; it is head-of-line
// when it is the front of the priority tier (if any priority waiters exist) or
// otherwise the front of the normal tier — the only waiter allowed to claim a
// freed agent, keeping dispatch strictly priority-then-FIFO.
type Queue interface {
	Enqueue(ctx context.Context, key string, priority bool) string
	IsHead(ctx context.Context, key, ticket string) bool
	Remove(ctx context.Context, key, ticket string)
	Depth(ctx context.Context, key string) int
}

// memQueue is the in-memory fallback.
type memQueue struct {
	mu       sync.Mutex
	priority map[string][]string
	normal   map[string][]string
}

// NewMemQueue returns the in-memory Queue backend.
func NewMemQueue() Queue {
	return &memQueue{priority: map[string][]string{}, normal: map[string][]string{}}
}

func (q *memQueue) Enqueue(_ context.Context, key string, priority bool) string {
	t := uuid.NewString()
	q.mu.Lock()
	if priority {
		q.priority[key] = append(q.priority[key], t)
	} else {
		q.normal[key] = append(q.normal[key], t)
	}
	q.mu.Unlock()
	return t
}

func (q *memQueue) IsHead(_ context.Context, key, ticket string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if p := q.priority[key]; len(p) > 0 {
		return p[0] == ticket
	}
	if n := q.normal[key]; len(n) > 0 {
		return n[0] == ticket
	}
	return false
}

func (q *memQueue) Remove(_ context.Context, key, ticket string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.priority[key] = removeTicket(q.priority[key], ticket); len(q.priority[key]) == 0 {
		delete(q.priority, key)
	}
	if q.normal[key] = removeTicket(q.normal[key], ticket); len(q.normal[key]) == 0 {
		delete(q.normal, key)
	}
}

func (q *memQueue) Depth(_ context.Context, key string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.priority[key]) + len(q.normal[key])
}

func removeTicket(q []string, t string) []string {
	for i, x := range q {
		if x == t {
			return append(q[:i], q[i+1:]...)
		}
	}
	return q
}

// redisQueue is the production backend: two Redis lists per team (priority,
// normal). RPUSH appends to the tail; the head (LINDEX 0) is the oldest waiter,
// so the lists are the FIFO and the ticket just identifies a waiter for
// IsHead/Remove. One global queue per team across all processes.
type redisQueue struct{ c *cache.Cache }

// NewRedisQueue returns the Redis-backed Queue (shared, durable).
func NewRedisQueue(c *cache.Cache) Queue { return &redisQueue{c: c} }

// queueTTL is a safety net so a fully-abandoned queue key self-expires; it's
// refreshed on every enqueue. Far longer than any real wait.
const queueTTL = time.Hour

func queuePKey(key string) string { return "tring:queue:p:" + key }
func queueNKey(key string) string { return "tring:queue:n:" + key }

func (q *redisQueue) Enqueue(ctx context.Context, key string, priority bool) string {
	t := uuid.NewString()
	if priority {
		q.c.RPush(ctx, queuePKey(key), t, queueTTL)
	} else {
		q.c.RPush(ctx, queueNKey(key), t, queueTTL)
	}
	return t
}

func (q *redisQueue) IsHead(ctx context.Context, key, ticket string) bool {
	if h, ok := q.c.LHead(ctx, queuePKey(key)); ok {
		return h == ticket // priority tier drains first
	}
	if h, ok := q.c.LHead(ctx, queueNKey(key)); ok {
		return h == ticket
	}
	return false
}

func (q *redisQueue) Remove(ctx context.Context, key, ticket string) {
	q.c.LRem(ctx, queuePKey(key), ticket)
	q.c.LRem(ctx, queueNKey(key), ticket)
}

func (q *redisQueue) Depth(ctx context.Context, key string) int {
	return q.c.LLen(ctx, queuePKey(key)) + q.c.LLen(ctx, queueNKey(key))
}
