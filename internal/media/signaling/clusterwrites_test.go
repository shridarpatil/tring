package signaling

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/cluster"
	"github.com/shridarpatil/tring/internal/media/room"
)

// clusterKeys reports which of this agent's per-node keys on node exist right
// now. Heartbeats in these tests are manual, so anything present was written by
// an immediate write.
func clusterKeys(t *testing.T, prefix, tenant, sip, node string) map[string]bool {
	t.Helper()
	rdb := redis.NewClient(cache.Options(os.Getenv("TRING_TEST_REDIS"), os.Getenv("TRING_TEST_REDIS_PASSWORD")))
	defer rdb.Close()
	ctx := context.Background()
	got := map[string]bool{}
	for name, key := range map[string]string{
		"online": prefix + "agents:" + tenant + ":" + node,
		"oncall": prefix + "count:oncall:" + tenant + ":" + sip + ":" + node,
		"dials":  prefix + "count:dials:" + tenant + ":" + sip + ":" + node,
	} {
		n, err := rdb.Exists(ctx, key).Result()
		if err != nil {
			t.Fatalf("exists %s: %v", key, err)
		}
		got[name] = n > 0
	}
	return got
}

// driveAgent does everything that used to write to Redis synchronously: an
// agent connects, joins a call and starts a dial.
func driveAgent(t *testing.T, s *Server) {
	t.Helper()
	openAgent(t, s, "t1", "100")
	s.setAgentOnCall("t1", "100", true)
	if _, ok := s.beginDial(room.RoomID("r1"), "t1|100"); !ok {
		t.Fatal("beginDial refused")
	}
}

// A node with no peers writes nothing on the call path: nobody reads it, and a
// stalled Redis would otherwise delay every connect and join.
func TestAloneSkipsImmediateClusterWrites(t *testing.T) {
	ca, _, prefix := newTestClusters(t)
	sa := newViewServer(t, ca)
	beatSnap(t, ca, cluster.Snapshot{}) // node-b never beats: A is alone
	if !ca.Alone() {
		t.Fatal("A not alone")
	}

	driveAgent(t, sa)

	for name, ok := range clusterKeys(t, prefix, "t1", "100", ca.ID()) {
		if ok {
			t.Errorf("%s key written while alone", name)
		}
	}
}

// With a peer the writes still happen right away, before the next heartbeat.
func TestPeersGetImmediateClusterWrites(t *testing.T) {
	ca, cb, prefix := newTestClusters(t)
	sa := newViewServer(t, ca)
	joinPair(t, sa, ca, cb, cluster.Snapshot{})

	driveAgent(t, sa)

	for name, ok := range clusterKeys(t, prefix, "t1", "100", ca.ID()) {
		if !ok {
			t.Errorf("%s key not written with a peer", name)
		}
	}
}
