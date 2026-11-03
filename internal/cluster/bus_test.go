package cluster

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
)

func rawClient(t *testing.T) *redis.Client {
	t.Helper()
	r := redis.NewClient(cache.Options(testRedisAddr(t), os.Getenv("TRING_TEST_REDIS_PASSWORD")))
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// runNode starts c.Run and returns a channel of handled messages. It returns
// once the node's bus subscription is live, so a Send right after can't be lost.
func runNode(t *testing.T, c *Cluster, snap func() Snapshot) <-chan Msg {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Msg, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, snap, func(m Msg) { got <- m })
	}()
	t.Cleanup(func() { cancel(); <-done })

	raw := rawClient(t)
	ch := c.busChannel(c.ID())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := raw.PubSubNumSub(context.Background(), ch).Result()
		if err == nil && n[ch] > 0 {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %s never subscribed", c.ID())
	return nil
}

func TestBusDelivers(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	empty := func() Snapshot { return Snapshot{} }
	gotA := runNode(t, a, empty)
	gotB := runNode(t, b, empty)

	want := Msg{Type: MsgRing, Tenant: "t1", SIP: "100", Room: "r1", Label: "Caller", Auto: true}
	if err := a.Send(context.Background(), b.ID(), want); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case m := <-gotB:
		want.V, want.From = 1, a.ID()
		if m != want {
			t.Fatalf("got %+v, want %+v", m, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b received nothing")
	}
	select {
	case m := <-gotA:
		t.Fatalf("a received %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBusDropsUnknownVersion(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	gotB := runNode(t, b, func() Snapshot { return Snapshot{} })

	raw := rawClient(t)
	ctx := context.Background()
	if err := raw.Publish(ctx, b.busChannel(b.ID()), `{"v":2,"type":"ring"}`).Err(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Publish(ctx, b.busChannel(b.ID()), `{"v":1,"type":"bogus"}`).Err(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Publish(ctx, b.busChannel(b.ID()), `not json`).Err(); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(ctx, b.ID(), Msg{Type: MsgDecline, Tenant: "t1", SIP: "100", Room: "r1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-gotB:
		if m.Type != MsgDecline || m.V != 1 {
			t.Fatalf("got %+v, want the valid decline", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b received nothing")
	}
	select {
	case m := <-gotB:
		t.Fatalf("extra message %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRunHeartbeats(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	b := newTestCluster(t, p, "b")
	runNode(t, a, func() Snapshot {
		return Snapshot{Online: map[string][]string{"t1": {"100"}}}
	})

	// Reads only consult the reader's cached live list, so b must heartbeat
	// after a's first beat to see a.
	ctx := context.Background()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := b.Heartbeat(ctx, Snapshot{}); err != nil {
			t.Fatal(err)
		}
		if _, ok := agents(t, b, "t1")["100"]; ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("b never saw agent 100 from a's Run heartbeats")
}

func TestNoWritesAfterClose(t *testing.T) {
	p := newTestPrefix(t)
	a := newTestCluster(t, p, "a")
	beat(t, a, Snapshot{})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.Heartbeat(ctx, Snapshot{Online: map[string][]string{"t1": {"100"}}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Heartbeat after close = %v, want ErrClosed", err)
	}
	a.SetOnline(ctx, "t1", "100", true)
	a.SetAway(ctx, "t1", "100", true)
	a.SetCount(ctx, OnCall, "t1", "100", 1)
	if err := a.Send(ctx, "b", Msg{Type: MsgRing}); err != nil {
		t.Fatalf("Send after close: %v", err)
	}

	raw := rawClient(t)
	keys, err := raw.Keys(ctx, p+"*").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys left after close: %v", keys)
	}
}
