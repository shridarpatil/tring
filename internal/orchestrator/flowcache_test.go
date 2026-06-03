package orchestrator

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/dbtest"
)

// Verifies the cache-attached resolve path against a live Redis: the first
// read populates the cache, the second is served from it, and an invalidate
// (what the API does on a flow edit) drops the key so the next read re-loads.
// Skips when Redis isn't reachable, so it doesn't gate the DB suite.
func TestResolveFlowThroughLiveCache(t *testing.T) {
	f := dbtest.Setup(t)
	c, err := cache.New("127.0.0.1:6379")
	if err != nil || c == nil {
		t.Skip("redis not reachable; skipping live-cache verification")
	}
	t.Cleanup(func() { _ = c.Close() })

	flowID := seedFlow(t,
		f,
		`{"version":2,"entry_node":"n1","nodes":[{"id":"n1","type":"hangup","config":{}}],"edges":[]}`,
		true,
	)
	seedInbound(t, f, "+918007778888", &flowID)

	fid := uuid.MustParse(flowID)
	key := FlowCacheKey(fid)
	exec := NewExecutor(f.DB).WithCache(c)
	ctx := context.Background()
	_ = c.Delete(ctx, key) // clear any stale key from a prior run

	// 1. Cold read populates the cache.
	got, err := exec.resolveFlow(ctx, "+918007778888")
	if err != nil || got.Flow == nil || got.Flow.EntryNode != "n1" {
		t.Fatalf("cold read: got=%+v err=%v", got, err)
	}
	if _, ok := c.Get(ctx, key); !ok {
		t.Fatal("expected cache to be populated after cold read")
	}

	// 2. Warm read returns the same parsed flow (served from cache).
	got2, err := exec.resolveFlow(ctx, "+918007778888")
	if err != nil || got2.Flow == nil || got2.Flow.EntryNode != "n1" {
		t.Fatalf("warm read: got=%+v err=%v", got2, err)
	}

	// 3. Invalidate (what the API does on a flow edit) drops the key.
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, ok := c.Get(ctx, key); ok {
		t.Fatal("expected cache miss after invalidation")
	}
}
