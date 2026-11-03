package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
)

const (
	testInterval = 100 * time.Millisecond
	testTTL      = time.Second
)

// testRedisAddr skips the test unless TRING_TEST_REDIS names a Redis to use.
func testRedisAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TRING_TEST_REDIS")
	if addr == "" {
		t.Skip("TRING_TEST_REDIS not set")
	}
	return addr
}

// newTestPrefix returns a random key prefix and removes every key under it when
// the test ends, so tests never collide and never touch anything else.
func newTestPrefix(t *testing.T) string {
	t.Helper()
	addr := testRedisAddr(t)
	prefix := "tringtest:" + uuid.NewString() + ":"
	t.Cleanup(func() { deletePrefix(t, addr, prefix) })
	return prefix
}

// deletePrefix SCAN+DELs the keys under prefix only (never FLUSHDB).
func deletePrefix(t *testing.T, addr, prefix string) {
	t.Helper()
	rdb := redis.NewClient(cache.Options(addr, os.Getenv("TRING_TEST_REDIS_PASSWORD")))
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
}

func newTestCluster(t *testing.T, prefix, id string) *Cluster {
	t.Helper()
	c, err := New(Config{
		Addr:     testRedisAddr(t),
		Password: os.Getenv("TRING_TEST_REDIS_PASSWORD"),
		ID:       id,
		Mode:     "all",
		Version:  "test",
		Prefix:   prefix,
		Interval: testInterval,
		TTL:      testTTL,
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Some tests Close early; a second Close only reports the closed client.
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func beat(t *testing.T, c *Cluster, s Snapshot) {
	t.Helper()
	if err := c.Heartbeat(context.Background(), s); err != nil {
		t.Fatalf("heartbeat %s: %v", c.ID(), err)
	}
}

func agents(t *testing.T, c *Cluster, tenant string) map[string]AgentState {
	t.Helper()
	m, err := c.Agents(context.Background(), tenant)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	return m
}

func TestNilClusterIsAlone(t *testing.T) {
	var c *Cluster
	ctx := context.Background()
	if !c.Alone() {
		t.Fatal("nil cluster should be alone")
	}
	if c.ID() != "" {
		t.Fatalf("ID = %q, want empty", c.ID())
	}
	c.SetOnline(ctx, "t1", "100", true)
	c.SetAway(ctx, "t1", "100", true)
	c.SetCount(ctx, OnCall, "t1", "100", 1)
	m, err := c.Agents(ctx, "t1")
	if m != nil || err != nil {
		t.Fatalf("Agents = %v, %v; want nil, nil", m, err)
	}
	if err := c.Heartbeat(ctx, Snapshot{}); err != nil {
		t.Fatalf("Heartbeat on nil: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
}

func TestNewEmptyAddrIsNil(t *testing.T) {
	c, err := New(Config{}, nil)
	if c != nil || err != nil {
		t.Fatalf("New(empty) = %v, %v; want nil, nil", c, err)
	}
}

func TestAloneUntilPeerSeen(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	if !a.Alone() || !b.Alone() {
		t.Fatal("both should be alone before any heartbeat")
	}
	beat(t, a, Snapshot{})
	if !a.Alone() {
		t.Fatal("a should be alone after heartbeating by itself")
	}
	beat(t, b, Snapshot{})
	beat(t, a, Snapshot{})
	if a.Alone() {
		t.Fatal("a should see b after both heartbeat")
	}
	if b.Alone() {
		t.Fatal("b saw a on its heartbeat, should not be alone")
	}
}

func TestAgentsAcrossNodes(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	sa := Snapshot{Online: map[string][]string{"t1": {"100"}}}
	sb := Snapshot{Counts: map[Kind]map[string]int{OnCall: {"t1|100": 1}}}
	for range 2 {
		beat(t, a, sa)
		beat(t, b, sb)
	}
	want := AgentState{Node: a.ID(), OnCall: 1}
	for _, c := range []*Cluster{a, b} {
		got := agents(t, c, "t1")
		if len(got) != 1 || got["100"] != want {
			t.Fatalf("%s: Agents = %+v, want only 100=%+v", c.ID(), got, want)
		}
	}
}

func TestSetOnlineVisibleBeforeHeartbeat(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	ctx := context.Background()
	beat(t, a, Snapshot{})
	beat(t, b, Snapshot{})
	beat(t, a, Snapshot{})

	b.SetOnline(ctx, "t1", "200", true)
	st, ok := agents(t, a, "t1")["200"]
	if !ok || st.Node != b.ID() {
		t.Fatalf("after SetOnline: 200 = %+v (present %v), want on node %s", st, ok, b.ID())
	}
	b.SetAway(ctx, "t1", "200", true)
	if st := agents(t, a, "t1")["200"]; !st.Away {
		t.Fatalf("after SetAway: 200 = %+v, want Away", st)
	}
	b.SetAway(ctx, "t1", "200", false)
	if st := agents(t, a, "t1")["200"]; st.Away {
		t.Fatalf("after SetAway(false): 200 = %+v, want not Away", st)
	}
	b.SetCount(ctx, Dials, "t1", "200", 2)
	if st := agents(t, a, "t1")["200"]; st.Dials != 2 {
		t.Fatalf("after SetCount: 200 = %+v, want Dials 2", st)
	}
	b.SetCount(ctx, Dials, "t1", "200", 0)
	if n, err := a.Count(ctx, Dials, "t1", "200"); err != nil || n != 0 {
		t.Fatalf("Count after SetCount(0) = %d, %v; want 0", n, err)
	}
	b.SetOnline(ctx, "t1", "200", false)
	if st, ok := agents(t, a, "t1")["200"]; ok {
		t.Fatalf("after SetOnline(false): 200 still present: %+v", st)
	}
}

func TestAgentsSameSIPOnTwoNodes(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	s := Snapshot{Online: map[string][]string{"t1": {"100"}}}
	beat(t, a, s)
	beat(t, b, s)
	beat(t, a, s)
	got := agents(t, a, "t1")
	if len(got) != 1 {
		t.Fatalf("Agents = %+v, want exactly one entry", got)
	}
	if n := got["100"].Node; n != a.ID() && n != b.ID() {
		t.Fatalf("100 on node %q, want a or b", n)
	}
}

func TestDeadNodeContributionExpires(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	ctx := context.Background()
	sa := Snapshot{Online: map[string][]string{"t1": {"100"}}}
	beat(t, b, Snapshot{Counts: map[Kind]map[string]int{OnCall: {"t1|100": 1}}})
	beat(t, a, sa)
	if n, err := a.Count(ctx, OnCall, "t1", "100"); err != nil || n != 1 {
		t.Fatalf("Count while b alive = %d, %v; want 1", n, err)
	}

	// b stops heartbeating without Close, as a crashed node would.
	time.Sleep(testTTL + 2*testInterval)
	beat(t, a, sa)
	if n, err := a.Count(ctx, OnCall, "t1", "100"); err != nil || n != 0 {
		t.Fatalf("Count after b expired = %d, %v; want 0", n, err)
	}
	if !a.Alone() {
		t.Fatal("a should be alone once b expired")
	}
}

func TestHeartbeatRestoresState(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	s := Snapshot{
		Online: map[string][]string{"t1": {"100"}},
		Away:   map[string][]string{"t1": {"100"}},
		Counts: map[Kind]map[string]int{OnCall: {"t1|100": 1}},
	}
	beat(t, a, s)
	deletePrefix(t, testRedisAddr(t), p) // simulates a Redis restart
	if got := agents(t, a, "t1"); len(got) != 0 {
		t.Fatalf("Agents after wipe = %+v, want empty", got)
	}
	beat(t, a, s)
	want := AgentState{Node: a.ID(), Away: true, OnCall: 1}
	if got := agents(t, a, "t1"); got["100"] != want {
		t.Fatalf("Agents after heartbeat = %+v, want 100=%+v", got, want)
	}
}

func TestAloneAfterFailedHeartbeat(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	beat(t, a, Snapshot{})
	beat(t, b, Snapshot{})
	beat(t, a, Snapshot{})
	if a.Alone() {
		t.Fatal("a should see b")
	}
	_ = a.Close()
	if err := a.Heartbeat(context.Background(), Snapshot{}); err == nil {
		t.Fatal("heartbeat on a closed cluster should fail")
	}
	if !a.Alone() {
		t.Fatal("a should be alone after a failed heartbeat")
	}
}

func TestCloseRemovesNode(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	ctx := context.Background()
	beat(t, a, Snapshot{})
	beat(t, b, Snapshot{
		Online: map[string][]string{"t1": {"100"}},
		Counts: map[Kind]map[string]int{OnCall: {"t1|100": 1}},
	})
	beat(t, a, Snapshot{})
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	beat(t, a, Snapshot{})
	if !a.Alone() {
		t.Fatal("a should be alone once b closed")
	}
	if got := agents(t, a, "t1"); len(got) != 0 {
		t.Fatalf("Agents after b closed = %+v, want empty", got)
	}
	if n, _ := a.Count(ctx, OnCall, "t1", "100"); n != 0 {
		t.Fatalf("Count after b closed = %d, want 0", n)
	}
}
