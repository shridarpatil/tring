package gateway

import (
	"log/slog"
	"net"
	"testing"

	"github.com/whatomate/tring/internal/presence"
)

// newTestSession builds a session backed by a throwaway pipe conn so registry
// behaviour can be exercised without a real WebSocket.
func newTestSession(tenant, sip string) (*agentSession, net.Conn) {
	a, b := net.Pipe()
	s := &agentSession{
		key:     agentKey{tenant: tenant, sip: sip},
		conn:    a,
		pending: map[string]*incomingCall{},
		log:     slog.Default(),
	}
	return s, b
}

// drain discards whatever a session writes to its peer so send() on the
// synchronous pipe never blocks the test.
func drain(peer net.Conn) {
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := peer.Read(buf); err != nil {
				return
			}
		}
	}()
}

// newTestRegistry / newTestGateway use a nil-cache presence store (disabled →
// all presence calls are no-ops), so tests don't need Redis.
func newTestRegistry() *agentRegistry { return newAgentRegistry(presence.New(nil)) }
func newTestGateway() *Gateway        { return &Gateway{agents: newTestRegistry(), log: slog.Default()} }

func TestRegistryAddRemove(t *testing.T) {
	r := newTestRegistry()
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()

	r.add(s)
	if got := r.sessionsFor(s.key); len(got) != 1 || got[0] != s {
		t.Fatalf("sessionsFor after add: got %v, want [s]", got)
	}
	if got := r.sessionsFor(agentKey{tenant: "t1", sip: "9999"}); got != nil {
		t.Fatalf("absent agent should have no sessions")
	}
	if got := r.sessionsFor(agentKey{tenant: "other", sip: "1001"}); got != nil {
		t.Fatalf("same sip in a different tenant must not match")
	}

	r.remove(s)
	if got := r.sessionsFor(s.key); got != nil {
		t.Fatalf("sessionsFor after remove should be empty")
	}
}

// Multiple sessions for one agent (several tabs) coexist — no eviction.
func TestRegistryMultipleSessionsCoexist(t *testing.T) {
	r := newTestRegistry()
	a, ap := newTestSession("t1", "1001")
	defer ap.Close()
	b, bp := newTestSession("t1", "1001")
	defer bp.Close()

	r.add(a)
	r.add(b)
	if got := r.sessionsFor(a.key); len(got) != 2 {
		t.Fatalf("both sessions should coexist (no eviction), got %d", len(got))
	}

	r.remove(a)
	if got := r.sessionsFor(b.key); len(got) != 1 || got[0] != b {
		t.Fatalf("after removing one, the other must remain, got %v", got)
	}
}

func TestRingAgentThenReject(t *testing.T) {
	g := newTestGateway()
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	drain(peer)
	g.agents.add(s)

	ic := g.ringAgent(s.key, "+919876543210", false)
	if ic == nil {
		t.Fatal("ringAgent returned nil for a live agent")
	}
	s.dispatch(agentMsg{Type: msgReject, CallID: ic.id})
	select {
	case <-ic.closed:
	default:
		t.Fatalf("reject should close the incoming call")
	}
}

func TestRingAgentNoSessions(t *testing.T) {
	g := newTestGateway()
	if ic := g.ringAgent(agentKey{tenant: "t1", sip: "ghost"}, "web", false); ic != nil {
		t.Fatalf("ringAgent for an offline agent must be nil, got %v", ic)
	}
}

func TestRingAgentThenOffer(t *testing.T) {
	g := newTestGateway()
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	drain(peer)
	g.agents.add(s)

	ic := g.ringAgent(s.key, "web", false)
	s.dispatch(agentMsg{Type: msgOffer, CallID: ic.id, SDP: "v=0..."})
	select {
	case of := <-ic.offer:
		if of.sess != s || of.sdp != "v=0..." {
			t.Fatalf("offer = %+v, want {s, v=0...}", of)
		}
	default:
		t.Fatalf("offer should be delivered to the waiting call")
	}
}

// Ring all sessions; the first accept wins (buffered offer chan), later accepts
// are dropped, and cancelOthers clears the losing tabs.
func TestRingAllFirstOfferWins(t *testing.T) {
	g := newTestGateway()
	a, ap := newTestSession("t1", "1001")
	defer ap.Close()
	drain(ap)
	b, bp := newTestSession("t1", "1001")
	defer bp.Close()
	drain(bp)
	g.agents.add(a)
	g.agents.add(b)

	ic := g.ringAgent(a.key, "web", false)
	if len(ic.ringers) != 2 {
		t.Fatalf("both tabs should be ringing, got %d", len(ic.ringers))
	}
	// b accepts first, then a — a's offer is dropped (buffer already full).
	b.dispatch(agentMsg{Type: msgOffer, CallID: ic.id, SDP: "fromB"})
	a.dispatch(agentMsg{Type: msgOffer, CallID: ic.id, SDP: "fromA"})
	of := <-ic.offer
	if of.sess != b {
		t.Fatalf("first offer (b) should win, got %v", of.sess)
	}

	// cancelOthers clears the loser from the ring + its pending.
	ic.cancelOthers(b)
	if _, ok := ic.ringers[a]; ok {
		t.Fatalf("a should have been cancelled")
	}
	a.mu.Lock()
	_, hasA := a.pending[ic.id]
	a.mu.Unlock()
	if hasA {
		t.Fatalf("a's pending should be cleared after cancelOthers")
	}
}

// Once a call is established (teardown registered), a browser hangup must tear
// the bridge down (which BYEs the FS leg) rather than just unblock the ring.
func TestHangupFiresTeardownWhenEstablished(t *testing.T) {
	g := newTestGateway()
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	drain(peer)
	g.agents.add(s)

	ic := g.ringAgent(s.key, "web", false)
	fired := false
	ic.setTeardown(func() { fired = true })

	s.dispatch(agentMsg{Type: msgHangup, CallID: ic.id})
	if !fired {
		t.Fatal("hangup on an established call must fire the bridge teardown")
	}
	select {
	case <-ic.closed:
		t.Fatal("teardown path must not also close ic.closed (that's the ringing path)")
	default:
	}
}

// The last session ringing a call closing ends the call (nobody can answer).
func TestSessionCloseDropsFromRing(t *testing.T) {
	g := newTestGateway()
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	drain(peer)
	g.agents.add(s)

	ic := g.ringAgent(s.key, "web", false)
	s.close()
	select {
	case <-ic.closed:
	default:
		t.Fatalf("last ringer closing must end the call")
	}
}

// With two tabs ringing, ONE closing must NOT end the call — the other can still
// answer.
func TestSessionCloseKeepsCallWhenSiblingRings(t *testing.T) {
	g := newTestGateway()
	a, ap := newTestSession("t1", "1001")
	defer ap.Close()
	drain(ap)
	b, bp := newTestSession("t1", "1001")
	defer bp.Close()
	drain(bp)
	g.agents.add(a)
	g.agents.add(b)

	ic := g.ringAgent(a.key, "web", false)
	a.close() // one tab closes
	select {
	case <-ic.closed:
		t.Fatal("call must not end while a sibling tab is still ringing")
	default:
	}
	// b can still answer.
	b.dispatch(agentMsg{Type: msgOffer, CallID: ic.id, SDP: "fromB"})
	if of := <-ic.offer; of.sess != b {
		t.Fatalf("b should still be able to answer")
	}
}
