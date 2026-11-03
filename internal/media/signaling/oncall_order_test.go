package signaling

import "testing"

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
