package signaling

import (
	"net"
	"testing"
	"time"
)

// A peer that stops reading (half-open TCP, or a client that never reads)
// fills its send buffer. Writes happen under the gateway-wide lock (ringing,
// withdrawing popups), so a write with no deadline froze all signaling until
// TCP gave up. A send must time out, and the socket then closes so every later
// send to it fails at once instead of stalling again.
func TestSendToNonReadingPeerTimesOut(t *testing.T) {
	server, client := net.Pipe() // synchronous: a write blocks until the client reads
	defer client.Close()
	c := &conn{Conn: server, writeTimeout: 50 * time.Millisecond}

	done := make(chan error, 1)
	go func() { done <- c.send(msg{Type: msgIncoming, Room: "r1"}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("send to a non-reading peer succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send blocked on a peer that never reads")
	}

	start := time.Now()
	if err := c.send(msg{Type: msgCancelRing, Room: "r1"}); err == nil {
		t.Fatal("send after a timed-out write succeeded; want the socket closed")
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Fatalf("second send took %v; want an immediate failure on the closed socket", d)
	}
}
