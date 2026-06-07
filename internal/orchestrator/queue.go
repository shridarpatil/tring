package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/cache"
)

// This file backs the Transfer-node queue: which callers are waiting for a team
// (Queue) and which agents are mid-ring (Reservations). Both have a Redis
// backend — the production path, shared across orchestrators that serve the same
// agents and durable across an orchestrator restart — and an in-memory backend
// used only when Redis isn't configured (host dev / unit tests).
//
// Why shared state matters: agents are a global resource. With >1 orchestrator
// (1:1 with FreeSWITCH), a call on unit A and a call on unit B can both want the
// same agent; only a shared reservation prevents a cross-unit double-ring, and a
// shared queue gives one global priority-then-FIFO line per team.

// ─── Reservations ───────────────────────────────────────────────────

// Reservations marks an agent as "being rung right now". Presence only flips to
// on_call when the agent ANSWERS, so without this two callers dequeuing at once
// both see the same idle agent as free and double-ring it.
type Reservations interface {
	reserve(ctx context.Context, key string, ttl time.Duration)
	release(ctx context.Context, key string)
	reserved(ctx context.Context, key string) bool
}

// memReservations is the in-memory fallback (one orchestrator, no Redis).
type memReservations struct {
	mu sync.Mutex
	m  map[string]time.Time // key -> expiry
}

func newMemReservations() *memReservations { return &memReservations{m: map[string]time.Time{}} }

func (r *memReservations) reserve(_ context.Context, key string, ttl time.Duration) {
	r.mu.Lock()
	r.m[key] = time.Now().Add(ttl)
	r.mu.Unlock()
}

func (r *memReservations) release(_ context.Context, key string) {
	r.mu.Lock()
	delete(r.m, key)
	r.mu.Unlock()
}

func (r *memReservations) reserved(_ context.Context, key string) bool {
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
// visible to every orchestrator sharing the cache. Redis handles expiry.
type redisReservations struct{ c *cache.Cache }

func newRedisReservations(c *cache.Cache) *redisReservations { return &redisReservations{c: c} }

func resvRedisKey(key string) string { return "tring:resv:" + key }

func (r *redisReservations) reserve(ctx context.Context, key string, ttl time.Duration) {
	r.c.Set(ctx, resvRedisKey(key), []byte("1"), ttl)
}
func (r *redisReservations) release(ctx context.Context, key string) {
	_ = r.c.Delete(ctx, resvRedisKey(key))
}
func (r *redisReservations) reserved(ctx context.Context, key string) bool {
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
	enqueue(ctx context.Context, key string, priority bool) string
	isHead(ctx context.Context, key, ticket string) bool
	remove(ctx context.Context, key, ticket string)
	depth(ctx context.Context, key string) int
}

// memQueue is the in-memory fallback.
type memQueue struct {
	mu       sync.Mutex
	priority map[string][]string
	normal   map[string][]string
}

func newMemQueue() *memQueue {
	return &memQueue{priority: map[string][]string{}, normal: map[string][]string{}}
}

func (q *memQueue) enqueue(_ context.Context, key string, priority bool) string {
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

func (q *memQueue) isHead(_ context.Context, key, ticket string) bool {
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

func (q *memQueue) remove(_ context.Context, key, ticket string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.priority[key] = removeTicket(q.priority[key], ticket); len(q.priority[key]) == 0 {
		delete(q.priority, key)
	}
	if q.normal[key] = removeTicket(q.normal[key], ticket); len(q.normal[key]) == 0 {
		delete(q.normal, key)
	}
}

func (q *memQueue) depth(_ context.Context, key string) int {
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
// isHead/remove. One global queue per team across all orchestrators.
type redisQueue struct{ c *cache.Cache }

func newRedisQueue(c *cache.Cache) *redisQueue { return &redisQueue{c: c} }

// queueTTL is a safety net so a fully-abandoned queue key self-expires; it's
// refreshed on every enqueue. Far longer than any real wait.
const queueTTL = time.Hour

func queuePKey(key string) string { return "tring:queue:p:" + key }
func queueNKey(key string) string { return "tring:queue:n:" + key }

func (q *redisQueue) enqueue(ctx context.Context, key string, priority bool) string {
	t := uuid.NewString()
	if priority {
		q.c.RPush(ctx, queuePKey(key), t, queueTTL)
	} else {
		q.c.RPush(ctx, queueNKey(key), t, queueTTL)
	}
	return t
}

func (q *redisQueue) isHead(ctx context.Context, key, ticket string) bool {
	if h, ok := q.c.LHead(ctx, queuePKey(key)); ok {
		return h == ticket // priority tier drains first
	}
	if h, ok := q.c.LHead(ctx, queueNKey(key)); ok {
		return h == ticket
	}
	return false
}

func (q *redisQueue) remove(ctx context.Context, key, ticket string) {
	q.c.LRem(ctx, queuePKey(key), ticket)
	q.c.LRem(ctx, queueNKey(key), ticket)
}

func (q *redisQueue) depth(ctx context.Context, key string) int {
	return q.c.LLen(ctx, queuePKey(key)) + q.c.LLen(ctx, queueNKey(key))
}
