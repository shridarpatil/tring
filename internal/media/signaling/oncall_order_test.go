package signaling

import (
	"testing"
	"time"
)

// An agent who has been told `joined` is already busy: anything that reacts to
// the join (a transfer or ring started the moment the SDK sees joined) must not
// pick them as a free agent.
func TestAgentOnCallBeforeJoined(t *testing.T) {
	h := newCallHarness(t)
	c := h.dial()
	h.send(c, msg{Type: msgJoin, Room: "r1"})
	h.readUntil(c, msgJoined)
	if !h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("agentOnCall = false right after joined, want true")
	}
}

// Busy follows the agent's room participant, not their call socket. A socket
// can close while the participant stays in the call (the reconnect grace
// window, or a media peer that keeps it connected) — prod 2026-11-04: an agent
// whose call socket closed mid-call read as free, was rung, and showed as idle.
// A reattach must not count the same call twice.
func TestAgentOnCallFollowsParticipantNotSocket(t *testing.T) {
	h := newCallHarness(t)
	tenant := h.tenant.String()

	c1 := h.dial()
	h.send(c1, msg{Type: msgJoin, Room: "r1"})
	joined := h.readUntil(c1, msgJoined)
	_ = c1.Close()
	time.Sleep(200 * time.Millisecond) // let the server see the close
	if !h.srv.agentOnCall(tenant, "alice") {
		t.Fatal("agent read as free while still in the call (socket closed, participant in grace)")
	}

	c2 := h.dial()
	h.send(c2, msg{Type: msgJoin, Room: "r1", Token: joined.Token})
	h.readUntil(c2, msgJoined)
	h.send(c2, msg{Type: msgBye})
	deadline := time.Now().Add(2 * time.Second)
	for h.srv.agentOnCall(tenant, "alice") {
		if time.Now().After(deadline) {
			t.Fatal("agent still on call after leaving (reattach counted twice?)")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
