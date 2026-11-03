package signaling

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/cluster"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// newClusterNode is one gateway node of a two-node cluster: a Server on its own
// SFU, serving /rtc over HTTP so agents can join rooms with a real token. Dev
// tokens make it an open-room server, as in newOpenRoomServer.
func newClusterNode(t *testing.T, c *cluster.Cluster) (*Server, string) {
	t.Helper()
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	s := New(Config{SFU: sf, Cluster: c, JWTSecret: authSecret, DevTokens: true})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, strings.Replace(ts.URL, "http://", "ws://", 1)
}

// waitSeen waits until s's view of tenant has every sip free on the given node
// ("" = s itself), i.e. heartbeats from both nodes have landed.
func waitSeen(t *testing.T, s *Server, tenant string, want map[string]string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := s.agentView(context.Background(), tenant)
		ok := true
		for sip, node := range want {
			if !v.free(sip) || v.node(sip) != node {
				ok = false
			}
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("view never showed %v free (states %+v)", want, v.states)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// joinRoom opens an /rtc call socket on url as sip and joins roomName, as the
// SDK does when the agent clicks answer. It returns once the server sends joined.
func joinRoom(t *testing.T, url string, tenant uuid.UUID, sip, roomName string) net.Conn {
	t.Helper()
	c, _, _, err := ws.Dial(context.Background(), url+"/rtc?token="+mintAgentTok(t, tenant, sip))
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	b, _ := json.Marshal(msg{Type: msgJoin, Room: roomName})
	if err := wsutil.WriteClientText(c, b); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	for {
		m := readMsg(t, c)
		if m.Type == msgJoined {
			return c
		}
		if m.Type == msgError {
			t.Fatalf("join %s: %+v", roomName, m)
		}
	}
}

// TestTwoNodeRingAnswerOnCall is the cluster end-to-end check: a call on B rings
// an agent whose presence socket is on A, the agent answers by joining the room
// on B, and from then on both nodes see the agent as on a call.
func TestTwoNodeRingAnswerOnCall(t *testing.T) {
	ca, cb, _ := newTestClusters(t)
	sa, _ := newClusterNode(t, ca)
	sb, urlB := newClusterNode(t, cb)
	runCluster(t, ca, sa)
	runCluster(t, cb, sb)

	tenantID := uuid.New()
	tenant := tenantID.String()
	agent := openAgent(t, sa, tenant, "100")
	waitSeen(t, sb, tenant, map[string]string{"100": ca.ID()})

	// The caller sits in the room on B.
	caller, err := sb.rooms.Join(room.RoomID(tenant+"/r1"), "Caller")
	if err != nil {
		t.Fatalf("caller join: %v", err)
	}
	defer caller.Leave()

	_, eff, cleanup := sb.Prepare(context.Background(), tenant+"/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100"}, "", "")
	defer cleanup()
	type result struct {
		answered bool
		err      error
	}
	res := make(chan result, 1)
	go func() {
		out, err := eff.Ring(context.Background(), []string{"100"}, 5*time.Second)
		res <- result{out.Answered, err}
	}()

	if m := agent.expect(msgIncoming); m.Room != "r1" {
		t.Fatalf("incoming = %+v, want room r1", m)
	}
	joinRoom(t, urlB, tenantID, "100", "r1")
	select {
	case r := <-res:
		if !r.answered || r.err != nil {
			t.Fatalf("Ring = answered %v err %v, want answered", r.answered, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ring did not return after the agent joined")
	}

	// A learns of the call on B through Redis.
	deadline := time.Now().Add(time.Second)
	for !sa.agentView(context.Background(), tenant).onCall("100") {
		if time.Now().After(deadline) {
			t.Fatal("A never saw agent 100 on call within 1s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A second call, from either node, finds no free agent.
	for name, s := range map[string]*Server{"A": sa, "B": sb} {
		cands, _, done := s.Prepare(context.Background(), tenant+"/r2", ivrdrive.Target{Label: "Next"}, false, []string{"100"}, "", "")
		got := cands(context.Background())
		done()
		if len(got) != 0 {
			t.Errorf("second ring from %s: candidates %v, want none", name, got)
		}
	}
}

// TestTwoNodeDeclineAdvances: a round_robin ring from B tries "100" on A first;
// its decline crosses the bus and B rings "101" (on B) at once, not after the
// ring window.
func TestTwoNodeDeclineAdvances(t *testing.T) {
	ca, cb, _ := newTestClusters(t)
	sa, _ := newClusterNode(t, ca)
	sb, _ := newClusterNode(t, cb)
	runCluster(t, ca, sa)
	runCluster(t, cb, sb)

	tenant := uuid.NewString()
	a100 := openAgent(t, sa, tenant, "100")
	a101 := openAgent(t, sb, tenant, "101")
	waitSeen(t, sb, tenant, map[string]string{"100": ca.ID(), "101": ""})

	const window = 5 * time.Second
	cands, eff, cleanup := sb.Prepare(context.Background(), tenant+"/r1", ivrdrive.Target{Label: "Caller"}, false, []string{"100", "101"}, "round_robin", "")
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The router's loop: ring the next candidate until one answers.
	go func() {
		ordered := []string{"100", "101"}
		for ctx.Err() == nil {
			out, err := eff.Ring(ctx, ordered, window)
			if err != nil || out.Answered {
				return
			}
			ordered = cands(ctx)
		}
	}()

	if m := a100.expect(msgIncoming); m.Room != "r1" {
		t.Fatalf("100 incoming = %+v, want room r1", m)
	}
	if m := a101.next(200 * time.Millisecond); m != nil {
		t.Fatalf("101 got %+v while 100 was ringing, want nothing", *m)
	}
	declined := time.Now()
	a100.send(msg{Type: msgDecline, Room: "r1"})

	m := a101.next(window / 2)
	if m == nil || m.Type != msgIncoming || m.Room != "r1" {
		t.Fatalf("101 got %+v after 100 declined, want incoming for r1", m)
	}
	if el := time.Since(declined); el >= 1500*time.Millisecond {
		t.Fatalf("101 rung %v after the decline, want < 1.5s", el)
	}
}
