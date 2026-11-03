package signaling

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/cluster"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
)

// agentSock is an agent's presence socket as the SDK sees it: every server
// message lands on msgs (read continuously, so a push never blocks the server).
type agentSock struct {
	t    *testing.T
	c    net.Conn
	msgs chan msg
}

// openAgent runs sip's presence socket on s and reads past `ready`.
func openAgent(t *testing.T, s *Server, tenant, sip string) *agentSock {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	go s.handleAgentPresence(&conn{Conn: serverConn}, tenant, sip)
	t.Cleanup(func() { _ = clientConn.Close() })
	a := &agentSock{t: t, c: clientConn, msgs: make(chan msg, 64)}
	go func() {
		defer close(a.msgs)
		for {
			data, err := wsutil.ReadServerText(clientConn)
			if err != nil {
				return
			}
			var m msg
			if json.Unmarshal(data, &m) == nil {
				a.msgs <- m
			}
		}
	}()
	if m := a.next(2 * time.Second); m == nil || m.Type != msgReady {
		t.Fatalf("first message = %+v, want ready", m)
	}
	return a
}

// next returns the next server message, nil if none arrives within d.
func (a *agentSock) next(d time.Duration) *msg {
	select {
	case m, ok := <-a.msgs:
		if !ok {
			return nil
		}
		return &m
	case <-time.After(d):
		return nil
	}
}

// expect waits for a message of type typ, failing on anything else.
func (a *agentSock) expect(typ string) msg {
	a.t.Helper()
	m := a.next(3 * time.Second)
	if m == nil {
		a.t.Fatalf("no %q message", typ)
	}
	if m.Type != typ {
		a.t.Fatalf("got %+v, want %q", *m, typ)
	}
	return *m
}

func (a *agentSock) send(m msg) {
	a.t.Helper()
	b, _ := json.Marshal(m)
	if err := wsutil.WriteClientText(a.c, b); err != nil {
		a.t.Fatalf("write: %v", err)
	}
}

// runCluster runs c's bus + heartbeat for s until the test ends.
func runCluster(t *testing.T, c *cluster.Cluster, s *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, s.ClusterSnapshot, s.HandleClusterMsg)
	}()
	// Registered after the clusters' Close, so it runs first.
	t.Cleanup(func() { cancel(); <-done })
}

// ringPair builds servers A and B on one cluster, running, with agent "100"'s
// presence socket on A, and waits until B sees that agent as free on A.
func ringPair(t *testing.T) (sb *Server, agent *agentSock) {
	t.Helper()
	ca, cb, _ := newTestClusters(t)
	sa := newViewServer(t, ca)
	sb = newViewServer(t, cb)
	runCluster(t, ca, sa)
	runCluster(t, cb, sb)
	agent = openAgent(t, sa, "t1", "100")

	deadline := time.Now().Add(5 * time.Second)
	for {
		v := sb.agentView(context.Background(), "t1")
		if !ca.Alone() && !cb.Alone() && v.free("100") && v.node("100") == ca.ID() {
			return sb, agent
		}
		if time.Now().After(deadline) {
			t.Fatalf("B never saw agent 100 free on A (a alone=%v, b alone=%v)", ca.Alone(), cb.Alone())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// busWatch subscribes to every bus channel under prefix and returns a function
// reporting whether anything was published within d.
func busWatch(t *testing.T, prefix string) func(d time.Duration) *redis.Message {
	t.Helper()
	rdb := redis.NewClient(cache.Options(os.Getenv("TRING_TEST_REDIS"), os.Getenv("TRING_TEST_REDIS_PASSWORD")))
	t.Cleanup(func() { _ = rdb.Close() })
	sub := rdb.PSubscribe(context.Background(), prefix+"node:*:bus")
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(context.Background()); err != nil {
		t.Fatalf("psubscribe: %v", err)
	}
	ch := sub.Channel()
	return func(d time.Duration) *redis.Message {
		select {
		case m := <-ch:
			return m
		case <-time.After(d):
			return nil
		}
	}
}

func TestRemoteRingReachesAgent(t *testing.T) {
	sb, agent := ringPair(t)
	_, eff, cleanup := sb.Prepare(context.Background(), "t1/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100"}, "", "")
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = eff.Ring(ctx, []string{"100"}, 3*time.Second) }()

	m := agent.expect(msgIncoming)
	if m.Room != "r1" || m.From != "Caller" || m.Auto {
		t.Fatalf("incoming = %+v, want room r1 from Caller", m)
	}
}

func TestRemoteDeclineAdvancesRouter(t *testing.T) {
	sb, agent := ringPair(t)
	_, eff, cleanup := sb.Prepare(context.Background(), "t1/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100"}, "", "")
	defer cleanup()

	start := time.Now()
	type result struct {
		answered bool
		err      error
	}
	res := make(chan result, 1)
	go func() {
		out, err := eff.Ring(context.Background(), []string{"100"}, 3*time.Second)
		res <- result{out.Answered, err}
	}()
	agent.expect(msgIncoming)
	agent.send(msg{Type: msgDecline, Room: "r1"})

	select {
	case r := <-res:
		if r.answered || r.err != nil {
			t.Fatalf("Ring = answered %v err %v, want not answered", r.answered, r.err)
		}
		if el := time.Since(start); el >= 1500*time.Millisecond {
			t.Fatalf("Ring returned after %v, want < 1.5s", el)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Ring did not return within 1.5s of a remote decline")
	}
}

func TestRemoteCancelOnNoAnswer(t *testing.T) {
	sb, agent := ringPair(t)
	_, eff, cleanup := sb.Prepare(context.Background(), "t1/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100"}, "round_robin", "")
	defer cleanup()
	go func() { _, _ = eff.Ring(context.Background(), []string{"100"}, 300*time.Millisecond) }()

	agent.expect(msgIncoming)
	if m := agent.expect(msgCancelRing); m.Room != "r1" {
		t.Fatalf("cancel = %+v, want room r1", m)
	}
}

// A decline or cancel that arrives after the ring ended (or for an agent not
// here) is dropped: no socket push, no bus message.
func TestClusterMsgLateDeclineAndCancelAreNoops(t *testing.T) {
	ca, cb, prefix := newTestClusters(t)
	_ = cb
	sa := newViewServer(t, ca)
	agent := openAgent(t, sa, "t1", "100")
	watch := busWatch(t, prefix)

	sa.HandleClusterMsg(cluster.Msg{Type: cluster.MsgDecline, From: "node-b", Tenant: "t1", SIP: "100", Room: "r1"})
	sa.HandleClusterMsg(cluster.Msg{Type: cluster.MsgRingCancel, From: "node-b", Tenant: "t1", SIP: "999", Room: "r1"})

	if m := agent.next(300 * time.Millisecond); m != nil {
		t.Fatalf("agent got %+v, want nothing", *m)
	}
	if m := watch(200 * time.Millisecond); m != nil {
		t.Fatalf("bus got %s: %s, want nothing", m.Channel, m.Payload)
	}
}

// An agent connected to the ringing node is rung over its socket directly,
// never via the bus — here the cluster is closed, so a bus hop couldn't deliver.
func TestLocalRingStaysLocal(t *testing.T) {
	ca, _, prefix := newTestClusters(t)
	watch := busWatch(t, prefix)
	_ = ca.Close()
	sa := newViewServer(t, ca)
	agent := openAgent(t, sa, "t1", "100")

	_, eff, cleanup := sa.Prepare(context.Background(), "t1/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100"}, "", "")
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = eff.Ring(ctx, []string{"100"}, 3*time.Second) }()
	if m := agent.expect(msgIncoming); m.Room != "r1" || m.From != "Caller" {
		t.Fatalf("incoming = %+v, want room r1 from Caller", m)
	}

	if !sa.pushRing("t1", "100", "", "r2", "Other", true) {
		t.Fatal("pushRing to a local agent = false")
	}
	if m := agent.expect(msgIncoming); m.Room != "r2" || !m.Auto {
		t.Fatalf("incoming = %+v, want room r2 auto", m)
	}
	if m := watch(200 * time.Millisecond); m != nil {
		t.Fatalf("bus got %s: %s, want nothing", m.Channel, m.Payload)
	}
}

// A node that crashed leaves its keys live until TTL, so the view still puts
// its agents there, but nobody reads its bus. The ring must count as not
// delivered right away — not hold the router for the whole ring window — and
// leave no popup or invite behind.
func TestRingToNodeWithoutListenerIsNotDelivered(t *testing.T) {
	ca, cb := newViewClusters(t)
	sa := newViewServer(t, ca)
	// B heartbeated agent 100 but never runs its bus: like a SIGKILLed node.
	joinPair(t, sa, ca, cb, cluster.Snapshot{Online: map[string][]string{"t1": {"100"}}})
	if n := sa.agentView(context.Background(), "t1").node("100"); n != cb.ID() {
		t.Fatalf("node(100) = %q, want %q", n, cb.ID())
	}

	_, eff, cleanup := sa.Prepare(context.Background(), "t1/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100"}, "", "")
	defer cleanup()
	start := time.Now()
	out, err := eff.Ring(context.Background(), []string{"100"}, 3*time.Second)
	if el := time.Since(start); el >= time.Second {
		t.Fatalf("Ring took %v, want < 1s (nobody could be rung)", el)
	}
	if out.Answered || err != nil {
		t.Fatalf("Ring = answered %v err %v, want not answered", out.Answered, err)
	}
	p := eff.(*pionRing)
	sa.mu.Lock()
	active, invited := p.active["100"], sa.invites["t1/r1"]["100"]
	sa.mu.Unlock()
	if active || invited {
		t.Fatalf("after a failed ring: active=%v invited=%v, want neither", active, invited)
	}
}
