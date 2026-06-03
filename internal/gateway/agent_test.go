package gateway

import (
	"log"
	"net"
	"testing"
)

// newTestSession builds a session backed by a throwaway pipe conn so registry
// behaviour can be exercised without a real WebSocket.
func newTestSession(tenant, sip string) (*agentSession, net.Conn) {
	a, b := net.Pipe()
	s := &agentSession{
		key:     agentKey{tenant: tenant, sip: sip},
		conn:    a,
		pending: map[string]*incomingCall{},
		log:     log.Default(),
	}
	return s, b
}

func TestRegistryAddLookupRemove(t *testing.T) {
	r := newAgentRegistry()
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()

	r.add(s)
	got, ok := r.lookup(agentKey{tenant: "t1", sip: "1001"})
	if !ok || got != s {
		t.Fatalf("lookup after add: got %v ok=%v, want the session", got, ok)
	}

	if _, ok := r.lookup(agentKey{tenant: "t1", sip: "9999"}); ok {
		t.Fatalf("lookup of absent agent should miss")
	}
	if _, ok := r.lookup(agentKey{tenant: "other", sip: "1001"}); ok {
		t.Fatalf("same sip in a different tenant must not match")
	}

	r.remove(s)
	if _, ok := r.lookup(s.key); ok {
		t.Fatalf("lookup after remove should miss")
	}
}

func TestRegistryReconnectEvictsOldSession(t *testing.T) {
	r := newAgentRegistry()
	old, oldPeer := newTestSession("t1", "1001")
	defer oldPeer.Close()
	r.add(old)

	// A new connection for the same agent replaces the old one and closes it.
	fresh, freshPeer := newTestSession("t1", "1001")
	defer freshPeer.Close()
	r.add(fresh)

	got, ok := r.lookup(fresh.key)
	if !ok || got != fresh {
		t.Fatalf("registry should hold the fresh session after reconnect")
	}
	// The evicted session's conn is closed — writes to it now fail.
	if _, err := old.conn.Write([]byte("x")); err == nil {
		t.Fatalf("old session conn should be closed after eviction")
	}
}

func TestRegistryRemoveStaleSessionKeepsReconnect(t *testing.T) {
	r := newAgentRegistry()
	old, oldPeer := newTestSession("t1", "1001")
	defer oldPeer.Close()
	r.add(old)

	fresh, freshPeer := newTestSession("t1", "1001")
	defer freshPeer.Close()
	r.add(fresh) // evicts old

	// The old session's deferred teardown calling remove must NOT delete the
	// fresh session that took its key.
	r.remove(old)
	if got, ok := r.lookup(fresh.key); !ok || got != fresh {
		t.Fatalf("stale remove clobbered the reconnected session")
	}
}

func TestStartIncomingThenReject(t *testing.T) {
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	// Drain whatever the session writes to the peer so send() doesn't block on
	// the synchronous pipe.
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := peer.Read(buf); err != nil {
				return
			}
		}
	}()

	ic := s.startIncoming("+919876543210")
	if ic == nil {
		t.Fatal("startIncoming returned nil")
	}
	// A reject from the browser ends the call.
	s.dispatch(agentMsg{Type: msgReject, CallID: ic.id})
	select {
	case <-ic.closed:
	default:
		t.Fatalf("reject should close the incoming call")
	}
}

func TestStartIncomingThenOffer(t *testing.T) {
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := peer.Read(buf); err != nil {
				return
			}
		}
	}()

	ic := s.startIncoming("web")
	s.dispatch(agentMsg{Type: msgOffer, CallID: ic.id, SDP: "v=0..."})
	select {
	case sdp := <-ic.offer:
		if sdp != "v=0..." {
			t.Fatalf("offer SDP = %q, want v=0...", sdp)
		}
	default:
		t.Fatalf("offer should be delivered to the waiting call")
	}
}

// Once a call is established (teardown registered), a browser hangup must tear
// the bridge down (which BYEs the FS leg) rather than just unblock the ring.
func TestHangupFiresTeardownWhenEstablished(t *testing.T) {
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := peer.Read(buf); err != nil {
				return
			}
		}
	}()

	ic := s.startIncoming("web")
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

func TestSessionCloseEndsPending(t *testing.T) {
	s, peer := newTestSession("t1", "1001")
	defer peer.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := peer.Read(buf); err != nil {
				return
			}
		}
	}()

	ic := s.startIncoming("web")
	s.close()
	select {
	case <-ic.closed:
	default:
		t.Fatalf("session close must end pending calls")
	}
}
