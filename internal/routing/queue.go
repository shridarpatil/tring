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

	"github.com/shridarpatil/tring/internal/cache"
)

// QueueKey scopes a team's waiting line to a tenant; ResKey scopes an agent's
// ring reservation to a tenant. Both planes build keys the same way so a shared
// Redis backend lines up across the FreeSWITCH and pion processes.
func QueueKey(tenantID, team string) string { return tenantID + "/" + team }
func ResKey(tenantID, sip string) string    { return tenantID + "/" + sip }

// ─── Reservations ───────────────────────────────────────────────────

// Reservations atomically claims an agent across ringing and active calls.
// Every mutation is scoped to a unique owner, so stale cleanup cannot free a
// newer call. Implementations fail closed when acquisition or renewal fails.
type Reservations interface {
	Reserve(ctx context.Context, key, owner string, ttl time.Duration) bool
	Renew(ctx context.Context, key, owner string, ttl time.Duration) bool
	Release(ctx context.Context, key, owner string)
	Reserved(ctx context.Context, key string) bool
}

type reservation struct {
	owner   string
	expires time.Time
}

type memReservations struct {
	mu sync.Mutex
	m  map[string]reservation
}

func NewMemReservations() Reservations { return &memReservations{m: map[string]reservation{}} }

func (r *memReservations) Reserve(ctx context.Context, key, owner string, ttl time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil || ttl <= 0 || time.Now().Before(r.m[key].expires) {
		return false
	}
	r.m[key] = reservation{owner, time.Now().Add(ttl)}
	return true
}

func (r *memReservations) Renew(ctx context.Context, key, owner string, ttl time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[key]
	if ctx.Err() != nil || ttl <= 0 || !ok || v.owner != owner || !time.Now().Before(v.expires) {
		return false
	}
	r.m[key] = reservation{owner, time.Now().Add(ttl)}
	return true
}

func (r *memReservations) Release(_ context.Context, key, owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[key].owner == owner {
		delete(r.m, key)
	}
}

func (r *memReservations) Reserved(_ context.Context, key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !time.Now().Before(r.m[key].expires) {
		delete(r.m, key)
		return false
	}
	return true
}

// Redis claims are shared by every process using this cache.
type redisReservations struct{ c *cache.Cache }

func NewRedisReservations(c *cache.Cache) Reservations { return &redisReservations{c: c} }
func resvRedisKey(key string) string                   { return "tring:resv:" + key }

func (r *redisReservations) Reserve(ctx context.Context, key, owner string, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	ok, err := r.c.SetNX(ctx, resvRedisKey(key), []byte(owner), ttl)
	return err == nil && ok
}
func (r *redisReservations) Renew(ctx context.Context, key, owner string, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	ok, err := r.c.CompareExpire(ctx, resvRedisKey(key), owner, ttl)
	return err == nil && ok
}
func (r *redisReservations) Release(ctx context.Context, key, owner string) {
	_, _ = r.c.CompareDelete(ctx, resvRedisKey(key), owner)
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
