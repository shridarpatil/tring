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

// When a cluster read fails, ringing decisions use this node's own maps, even
// with live peers. The failure is a WRONGTYPE count key behind a redis.Nil in
// the same pipeline, which once read as a clean (under)count. Three nodes, so a
// masked read differs from the local answer: C holds 200's real counts, B's
// keys for 200 are the wrong type, A's are missing.
func TestAgentViewLocalWhenClusterReadFails(t *testing.T) {
	ca, cb, prefix := newTestClusters(t)
	cc, err := cluster.New(cluster.Config{
		Addr: os.Getenv("TRING_TEST_REDIS"), Password: os.Getenv("TRING_TEST_REDIS_PASSWORD"),
		ID: "node-c", Mode: "all", Version: "test",
		Prefix: prefix, Interval: 100 * time.Millisecond, TTL: time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	sa := newViewServer(t, ca)
	sa.agents["t1"] = map[string]*conn{"100": {}}
	beatSnap(t, cb, cluster.Snapshot{Online: map[string][]string{"t1": {"200"}}})
	beatSnap(t, cc, cluster.Snapshot{Counts: map[cluster.Kind]map[string]int{
		cluster.OnCall: {"t1|200": 1},
		cluster.Dials:  {"t1|200": maxAgentOutbound},
	}})
	beatSnap(t, ca, sa.ClusterSnapshot())
	if ca.Alone() {
		t.Fatal("A is alone")
	}

	rdb := redis.NewClient(cache.Options(os.Getenv("TRING_TEST_REDIS"), os.Getenv("TRING_TEST_REDIS_PASSWORD")))
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	for _, kind := range []cluster.Kind{cluster.OnCall, cluster.Dials} {
		key := prefix + "count:" + string(kind) + ":t1:200:node-b"
		if err := rdb.HSet(ctx, key, "x", "1").Err(); err != nil {
			t.Fatal(err)
		}
	}

	v := sa.agentView(ctx, "t1")
	if v.online("200") {
		t.Fatal("online(200) = true, want the local view (200 is only on B)")
	}
	if !v.free("100") || v.node("100") != "" {
		t.Fatalf("local agent 100: free=%v node=%q, want free and local", v.free("100"), v.node("100"))
	}
	if sa.agentOnCall("t1", "200") {
		t.Fatal("agentOnCall(200) = true, want false from the local maps")
	}
	done, ok := sa.beginDial("room-x", "t1|200")
	if !ok {
		t.Fatal("beginDial refused, want the local count (0) to allow it")
	}
	done()
}
