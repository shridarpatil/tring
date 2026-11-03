package routing

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cache"
)

// Two counters on separate Redis clients stand in for two gateway nodes: the
// rotation must be one shared sequence, not one per node.
func TestRedisRRSharedAcrossCounters(t *testing.T) {
	a := testRedis(t)
	b, err := cache.New(os.Getenv("TRING_TEST_REDIS"), os.Getenv("TRING_TEST_REDIS_PASSWORD"))
	if err != nil || b == nil {
		t.Skipf("second redis client: %v", err)
	}
	defer b.Close()
	key := "t/team-" + uuid.NewString()
	t.Cleanup(func() { _ = a.Delete(context.Background(), "tring:rr:"+key) })
	rrs := []RRCounter{NewRedisRR(a), NewRedisRR(b)}
	want := []int{0, 1, 2, 0, 1, 2}
	for i, w := range want {
		if got := rrs[i%2].Next(key, 3); got != w {
			t.Fatalf("call %d = %d, want %d", i, got, w)
		}
	}
}

func TestRedisRRZeroN(t *testing.T) {
	c := testRedis(t)
	key := "t/team-" + uuid.NewString()
	if got := NewRedisRR(c).Next(key, 0); got != 0 {
		t.Fatalf("Next(key, 0) = %d, want 0", got)
	}
	if _, ok := c.Get(context.Background(), "tring:rr:"+key); ok {
		t.Fatal("n=0 touched Redis")
	}
}

func TestRedisRRNilCacheFallsBackToMemory(t *testing.T) {
	rr := NewRedisRR(nil)
	for i, w := range []int{0, 1, 0, 1} {
		if got := rr.Next("k", 2); got != w {
			t.Fatalf("call %d = %d, want %d", i, got, w)
		}
	}
}
