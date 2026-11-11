package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"

	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/routing"
)

func waitAgentClaimReleased(t *testing.T, h *callHarness) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.srv.reservations.Reserved(context.Background(), routing.ResKey(h.tenant.String(), "alice")) {
		if time.Now().After(deadline) {
			t.Fatal("call leaked its agent claim")
		}
		time.Sleep(time.Millisecond)
	}
}

func startTestInbound(t *testing.T, h *callHarness, name string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.srv.agentRouter.RingNoHandoff(ctx, h.tenant.String()+"/"+name, ivrdrive.Target{AgentSIP: "alice"})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("inbound router did not stop")
		}
	})
	return cancel
}

func TestConcurrentOutboundOnlyOneRings(t *testing.T) {
	h := newCallHarness(t)
	start := make(chan struct{})
	result := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := h.srv.Call(context.Background(), h.tenant, CallRequest{AgentSIP: "alice", To: "9812345678", Label: "Lead 42"})
			result <- err
		}()
	}
	close(start)
	accepted, busy := 0, 0
	for range 2 {
		err := <-result
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, errAgentBusy):
			busy++
		default:
			t.Fatalf("Call failed: %v", err)
		}
	}
	if accepted != 1 || busy != 1 {
		t.Fatalf("accepted=%d busy=%d, want 1 each", accepted, busy)
	}
	incoming := h.readUntil(h.pres, msgIncoming)
	h.send(h.pres, msg{Type: msgDecline, Room: incoming.Room})
	waitEnd(t, h.cl)
	waitAgentClaimReleased(t, h)
}

func TestInboundRingBlocksOutbound(t *testing.T) {
	h := newCallHarness(t)
	stop := startTestInbound(t, h, "inbound")
	h.readUntil(h.pres, msgIncoming)
	_, err := h.srv.Call(context.Background(), h.tenant, CallRequest{AgentSIP: "alice", To: "9812345678"})
	if !errors.Is(err, errAgentBusy) {
		t.Fatalf("Call while inbound ringing = %v, want busy", err)
	}
	stop()
	waitAgentClaimReleased(t, h)
}

func TestOutboundRingQueuesInboundUntilDeclined(t *testing.T) {
	h := newCallHarness(t)
	h.call()
	first := h.readUntil(h.pres, msgIncoming)
	startTestInbound(t, h, "queued-inbound")
	// No second ring may reach the widget while the first remains unanswered.
	h.pres.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for {
		data, err := wsutil.ReadServerText(h.pres)
		if err != nil {
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				t.Fatalf("presence read failed: %v", err)
			}
			break
		}
		var m msg
		if json.Unmarshal(data, &m) == nil && m.Type == msgIncoming {
			t.Fatal("inbound rang over pending outbound")
		}
	}
	h.send(h.pres, msg{Type: msgDecline, Room: first.Room})
	waitEnd(t, h.cl)
	if next := h.readUntil(h.pres, msgIncoming); next.Room != "queued-inbound" {
		t.Fatalf("next incoming = %+v", next)
	}
}

func TestInboundAnswerRetainsClaimUntilHangup(t *testing.T) {
	h := newCallHarness(t)
	startTestInbound(t, h, "inbound")
	incoming := h.readUntil(h.pres, msgIncoming)
	agent := h.dial()
	h.send(agent, msg{Type: msgJoin, Room: incoming.Room})
	h.readUntil(agent, msgJoined)
	// The router has finished, but its lease must now belong to the participant.
	time.Sleep(30 * time.Millisecond)
	if !h.srv.reservations.Reserved(context.Background(), routing.ResKey(h.tenant.String(), "alice")) {
		t.Fatal("answer dropped agent claim")
	}
	_, err := h.srv.Call(context.Background(), h.tenant, CallRequest{AgentSIP: "alice", To: "9812345678"})
	if !errors.Is(err, errAgentBusy) {
		t.Fatalf("Call while connected = %v, want busy", err)
	}
	h.send(agent, msg{Type: msgBye})
	waitAgentClaimReleased(t, h)
}

func TestAgentClaimSurvivesReconnectGrace(t *testing.T) {
	h := newCallHarness(t)
	joined := dropCall(h)
	if !h.srv.reservations.Reserved(context.Background(), routing.ResKey(h.tenant.String(), "alice")) {
		t.Fatal("dropped socket released the call's claim")
	}
	// A different call room cannot bypass the claim via a direct join either.
	other := h.dial()
	h.send(other, msg{Type: msgJoin, Room: "second-room"})
	if got := h.readUntil(other, msgError); got.Reason != errAgentBusy.Error() {
		t.Fatalf("second join = %+v", got)
	}
	resumed := h.dial()
	h.send(resumed, msg{Type: msgJoin, Room: "r1", Token: joined.Token})
	h.readUntil(resumed, msgJoined)
	h.send(resumed, msg{Type: msgBye})
	waitAgentClaimReleased(t, h)
}
