package routing

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/presence"
)

// RRCounter yields a rotating start offset per key for the round_robin strategy.
// NewMemRR is per-process (load-spreading, not strict fairness, matching the
// trunk round_robin precedent); NewRedisRR rotates consistently across gateway
// nodes sharing one Redis.
type RRCounter interface {
	Next(key string, n int) int
}

type memRR struct {
	mu sync.Mutex
	m  map[string]int
}

// NewMemRR returns the in-process round-robin counter.
func NewMemRR() RRCounter { return &memRR{m: map[string]int{}} }

type redisRR struct {
	c        *cache.Cache
	fallback RRCounter
}

// NewRedisRR rotates through a shared Redis counter so every gateway node
// advances the same per-team position. If Redis errors (or c is nil) it falls
// back to an in-process counter: ringing must not stop because Redis is down.
func NewRedisRR(c *cache.Cache) RRCounter {
	return &redisRR{c: c, fallback: NewMemRR()}
}

func (r *redisRR) Next(key string, n int) int {
	if n <= 0 {
		return 0
	}
	v, err := r.c.Incr(context.Background(), "tring:rr:"+key)
	if err != nil {
		return r.fallback.Next(key, n)
	}
	return int((v - 1) % int64(n))
}

func (r *memRR) Next(key string, n int) int {
	if n <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.m[key] % n
	r.m[key] = (r.m[key] + 1) % (1 << 30) // bounded so the counter never overflows
	return i
}

// OrderByStrategy reorders a team's available members for ringing:
//   - round_robin: rotate the start by a per-team counter (load spreading)
//   - longest_wait: idle-longest first (by Redis freed-at; missing = longest)
//   - sequential / ring_all / "": keep membership-position order
func OrderByStrategy(ctx context.Context, pres *presence.Store, rr RRCounter, tenantID, team, strategy string, sips []string) []string {
	switch strategy {
	case "round_robin":
		if len(sips) > 1 && rr != nil {
			start := rr.Next(tenantID+"/"+team, len(sips))
			sips = append(append([]string{}, sips[start:]...), sips[:start]...)
		}
	case "longest_wait":
		if pres != nil && pres.Enabled() {
			freed := make(map[string]time.Time, len(sips))
			for _, sip := range sips {
				if t, ok := pres.FreedAt(ctx, tenantID, sip); ok {
					freed[sip] = t
				} // absent → zero time → sorts first (idle longest)
			}
			sort.SliceStable(sips, func(i, j int) bool {
				return freed[sips[i]].Before(freed[sips[j]])
			})
		}
	}
	return sips
}
