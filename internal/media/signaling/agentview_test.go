package signaling

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/cluster"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// newViewServer builds a Server on its own SFU with the given cluster (nil →
// single node).
func newViewServer(t *testing.T, c *cluster.Cluster) *Server {
	t.Helper()
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	return New(Config{SFU: sf, Cluster: c})
}

// newViewClusters returns two clusters on a fresh key prefix (removed when the
// test ends), skipping unless TRING_TEST_REDIS is set.
func newViewClusters(t *testing.T) (ca, cb *cluster.Cluster) {
	t.Helper()
	ca, cb, _ = newTestClusters(t)
	return ca, cb
}

// newTestClusters is newViewClusters plus the key prefix, for tests that watch
// the bus channels themselves.
func newTestClusters(t *testing.T) (ca, cb *cluster.Cluster, prefix string) {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		t.Skip("TRING_TEST_REDIS not set")
	}
	pass := os.Getenv("TRING_TEST_REDIS_PASSWORD")
	prefix = "tringtest:" + uuid.NewString() + ":"
	t.Cleanup(func() {
		rdb := redis.NewClient(cache.Options(addr, pass))
		defer rdb.Close()
		ctx := context.Background()
		iter := rdb.Scan(ctx, 0, prefix+"*", 100).Iterator()
		for iter.Next(ctx) {
			if err := rdb.Del(ctx, iter.Val()).Err(); err != nil {
				t.Errorf("del %s: %v", iter.Val(), err)
			}
		}
		if err := iter.Err(); err != nil {
			t.Errorf("scan: %v", err)
		}
	})
	mk := func(id string) *cluster.Cluster {
		c, err := cluster.New(cluster.Config{
			Addr: addr, Password: pass, ID: id, Mode: "all", Version: "test",
			Prefix: prefix, Interval: 100 * time.Millisecond, TTL: time.Second,
		}, nil)
		if err != nil {
			t.Fatalf("cluster.New: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	return mk("node-a"), mk("node-b"), prefix
}

func beatSnap(t *testing.T, c *cluster.Cluster, s cluster.Snapshot) {
	t.Helper()
	if err := c.Heartbeat(context.Background(), s); err != nil {
		t.Fatalf("heartbeat %s: %v", c.ID(), err)
	}
}

// joinPair heartbeats A (with sa's snapshot) and B (with snapB) until both see
// each other, i.e. neither is Alone.
func joinPair(t *testing.T, sa *Server, ca, cb *cluster.Cluster, snapB cluster.Snapshot) {
	t.Helper()
	beatSnap(t, ca, sa.ClusterSnapshot())
	beatSnap(t, cb, snapB)
	beatSnap(t, ca, sa.ClusterSnapshot())
	if ca.Alone() || cb.Alone() {
		t.Fatalf("still alone: a=%v b=%v", ca.Alone(), cb.Alone())
	}
}

func TestAgentViewLocalWithoutCluster(t *testing.T) {
	s := newViewServer(t, nil)
	s.agents["t1"] = map[string]*conn{"100": {}}
	v := s.agentView(context.Background(), "t1")
	if !v.free("100") {
		t.Fatal("free(100) = false, want true")
	}
	if n := v.node("100"); n != "" {
		t.Fatalf("node(100) = %q, want local", n)
	}
}

func TestAgentViewSeesRemoteAgent(t *testing.T) {
	ca, cb := newViewClusters(t)
	sa := newViewServer(t, ca)
	sb := newViewServer(t, cb)
	sa.agents["t1"] = map[string]*conn{"100": {}}
	joinPair(t, sa, ca, cb, sb.ClusterSnapshot())

	v := sb.agentView(context.Background(), "t1")
	if !v.online("100") || !v.free("100") {
		t.Fatalf("B's view: online=%v free=%v, want both true", v.online("100"), v.free("100"))
	}
	if n := v.node("100"); n != ca.ID() {
		t.Fatalf("node(100) = %q, want %q", n, ca.ID())
	}
}

func TestAgentViewRemoteOnCallIsBusy(t *testing.T) {
	ca, cb := newViewClusters(t)
	sa := newViewServer(t, ca)
	sb := newViewServer(t, cb)
	sa.agents["t1"] = map[string]*conn{"100": {}}
	joinPair(t, sa, ca, cb, sb.ClusterSnapshot())
	cb.SetCount(context.Background(), cluster.OnCall, "t1", "100", 1)

	if sa.agentView(context.Background(), "t1").free("100") {
		t.Fatal("A's view free(100) = true, want false (on a call on B)")
	}
	if sa.targetReachable(context.Background(), "t1", "100", "") {
		t.Fatal("targetReachable = true, want false")
	}
}

func TestAgentViewLocalWhenHeartbeatFails(t *testing.T) {
	ca, cb := newViewClusters(t)
	sa := newViewServer(t, ca)
	sb := newViewServer(t, cb)
	sa.agents["t1"] = map[string]*conn{"100": {}}
	joinPair(t, sa, ca, cb, sb.ClusterSnapshot())

	_ = ca.Close()
	if err := ca.Heartbeat(context.Background(), sa.ClusterSnapshot()); err == nil {
		t.Fatal("heartbeat after Close succeeded")
	}
	v := sa.agentView(context.Background(), "t1")
	if !v.free("100") {
		t.Fatal("free(100) = false, want true from the local maps")
	}
	if n := v.node("100"); n != "" {
		t.Fatalf("node(100) = %q, want local", n)
	}
}

func TestPresenceTickUsesGlobalOnCall(t *testing.T) {
	ca, cb := newViewClusters(t)
	sa := newViewServer(t, ca)
	sa.agents["t1"] = map[string]*conn{"100": {}}
	joinPair(t, sa, ca, cb, cluster.Snapshot{
		Counts: map[cluster.Kind]map[string]int{cluster.OnCall: {"t1|100": 1}},
	})
	if !sa.agentOnCall("t1", "100") {
		t.Fatal("agentOnCall = false, want true (on a call on B)")
	}
}

func TestClusterSnapshot(t *testing.T) {
	s := newViewServer(t, nil)
	c := &conn{}
	s.agents["t1"] = map[string]*conn{"100": c}
	s.setAway(c, "t1", "100", true)
	s.setAgentOnCall("t1", "100", true)

	snap := s.ClusterSnapshot()
	if got := snap.Online["t1"]; !reflect.DeepEqual(got, []string{"100"}) {
		t.Fatalf("Online[t1] = %v, want [100]", got)
	}
	if got := snap.Away["t1"]; !reflect.DeepEqual(got, []string{"100"}) {
		t.Fatalf("Away[t1] = %v, want [100]", got)
	}
	if got := snap.Counts[cluster.OnCall]["t1|100"]; got != 1 {
		t.Fatalf("Counts[OnCall][t1|100] = %d, want 1", got)
	}
}

// A failed immediate write can leave Redis saying 0 while this node knows the
// agent is on a call (or Away); the local truth must win until the heartbeat.
func TestAgentViewLocalOnCallWinsOverStaleCluster(t *testing.T) {
	ca, cb := newViewClusters(t)
	sa := newViewServer(t, ca)
	sb := newViewServer(t, cb)
	c := &conn{}
	sa.agents["t1"] = map[string]*conn{"100": c, "200": {}}
	joinPair(t, sa, ca, cb, sb.ClusterSnapshot())

	ctx := context.Background()
	sa.setAgentOnCall("t1", "100", true)
	ca.SetCount(ctx, cluster.OnCall, "t1", "100", 0) // stale: lost the write
	sa.mu.Lock()
	sa.away["t1|200"] = true // Away locally; Redis never heard
	sa.mu.Unlock()

	v := sa.agentView(ctx, "t1")
	if v.free("100") {
		t.Fatal("free(100) = true, want false (on a call here)")
	}
	if !sa.agentOnCall("t1", "100") {
		t.Fatal("agentOnCall(100) = false, want true")
	}
	if v.free("200") {
		t.Fatal("free(200) = true, want false (Away here)")
	}
}
