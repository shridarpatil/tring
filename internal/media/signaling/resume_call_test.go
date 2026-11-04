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
	"github.com/golang-jwt/jwt/v5"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// dialAs opens an /rtc socket as another agent of the harness tenant.
func (h *callHarness) dialAs(sip string) net.Conn {
	h.t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant": h.tenant.String(), "sip_user": sip,
		"iss": jwtIssuer, "aud": agentAudience,
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(testControlSecret))
	if err != nil {
		h.t.Fatal(err)
	}
	c, _, _, err := ws.Dial(context.Background(), h.url+"/rtc?token="+tok)
	if err != nil {
		h.t.Fatalf("ws dial: %v", err)
	}
	h.t.Cleanup(func() { _ = c.Close() })
	return c
}

// dropCall joins alice into room r1, then closes the call socket the way a page
// refresh does (no bye), and waits until the server holds her slot.
func dropCall(h *callHarness) msg {
	h.t.Helper()
	c := h.dial()
	h.send(c, msg{Type: msgJoin, Room: "r1"})
	joined := h.readUntil(c, msgJoined)
	_ = c.Close()
	waitState(h, "r1", room.StateReconnecting)
	return joined
}

func waitState(h *callHarness, name string, want room.State) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if r, ok := h.srv.rooms.Get(room.RoomID(h.tenant.String() + "/" + name)); ok {
			for _, p := range r.Participants() {
				if p.State() == want {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no participant in %s reached %v", name, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A page refresh mid-call drops both of the agent's sockets. When the new page's
// presence socket registers, the server names the call it is holding for them,
// and a join to that room (no session token — the page lost it) puts them back
// in the same slot rather than refusing it as uninvited.
func TestRefreshedAgentIsToldAndResumesCall(t *testing.T) {
	h := newCallHarness(t)
	joined := dropCall(h)
	_ = h.pres.Close()

	pres := h.dial()
	h.send(pres, msg{Type: msgRegister})
	if m := h.readUntil(pres, msgResumeCall); m.Room != "r1" || m.Phone || m.Auto {
		t.Fatalf("resume-call %+v, want room r1, a web call, not the agent's own", m)
	}
	// Between the reload and the rejoin the agent must stay busy, so the ring
	// candidate check (onCall) can't land another call on them.
	if !h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("refreshed agent read as free before rejoining their call")
	}

	c := h.dial()
	h.send(c, msg{Type: msgJoin, Room: "r1"})
	again := h.readUntil(c, msgJoined)
	if again.Participant != joined.Participant {
		t.Fatalf("rejoined as %s, want the held slot %s", again.Participant, joined.Participant)
	}
	if !h.srv.agentOnCall(h.tenant.String(), "alice") {
		t.Fatal("agent read as free after resuming their call")
	}

	// Still one call: a bye frees the agent (the rejoin didn't count it twice).
	h.send(c, msg{Type: msgBye})
	deadline := time.Now().Add(2 * time.Second)
	for h.srv.agentOnCall(h.tenant.String(), "alice") {
		if time.Now().After(deadline) {
			t.Fatal("agent still on call after bye (resume counted the call twice?)")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Only the agent whose slot is held may take it back.
func TestResumeRefusedForAnotherAgent(t *testing.T) {
	h := newCallHarness(t)
	dropCall(h)

	c := h.dialAs("bob")
	h.send(c, msg{Type: msgJoin, Room: "r1"})
	if m := h.readUntil(c, msgError); m.Reason != "not invited to this call" {
		t.Fatalf("bob's join: %q, want not invited", m.Reason)
	}
}

// A live call can't be taken over from a second tab: only a dropped slot resumes.
func TestResumeRefusedWhileCallSocketLive(t *testing.T) {
	h := newCallHarness(t)
	c1 := h.dial()
	h.send(c1, msg{Type: msgJoin, Room: "r1"})
	h.readUntil(c1, msgJoined)

	pres := h.dial()
	h.send(pres, msg{Type: msgRegister})
	h.readUntil(pres, msgReady)
	_ = pres.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		m, err := tryRead(pres)
		if err != nil {
			break // no resume-call: good
		}
		if m.Type == msgResumeCall {
			t.Fatal("resume-call sent for a call whose socket is still live")
		}
	}

	c2 := h.dial()
	h.send(c2, msg{Type: msgJoin, Room: "r1"})
	if m := h.readUntil(c2, msgError); m.Reason != "not invited to this call" {
		t.Fatalf("second tab's join: %q, want not invited", m.Reason)
	}
}

// Past the grace window the slot is gone: the new page is told nothing.
func TestNoResumeAfterGrace(t *testing.T) {
	h := newCallHarness(t)
	h.srv = newGraceServer(t, 100*time.Millisecond)
	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	h.url = strings.Replace(ts.URL, "http://", "ws://", 1)

	dropCall(h)
	deadline := time.Now().Add(2 * time.Second)
	for h.srv.agentOnCall(h.tenant.String(), "alice") {
		if time.Now().After(deadline) {
			t.Fatal("alice's slot never expired")
		}
		time.Sleep(10 * time.Millisecond)
	}

	pres := h.dial()
	h.send(pres, msg{Type: msgRegister})
	h.readUntil(pres, msgReady)
	_ = pres.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		m, err := tryRead(pres)
		if err != nil {
			break
		}
		if m.Type == msgResumeCall {
			t.Fatal("resume-call sent after the grace window expired")
		}
	}
}

func newGraceServer(t *testing.T, grace time.Duration) *Server {
	t.Helper()
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	return New(Config{SFU: sf, JWTSecret: []byte(testControlSecret), Grace: grace})
}

// tryRead reads one message, returning the error instead of failing (for
// asserting that nothing more arrives before the conn's deadline).
func tryRead(c net.Conn) (msg, error) {
	var m msg
	data, err := wsutil.ReadServerText(c)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

// A reloaded click-to-call is resumed as what it was: the agent's own outbound
// phone call, so the widget keeps its keypad and hides screen share.
func TestResumeCallMarksOwnPhoneCall(t *testing.T) {
	client := newConnectLeg("out-client")
	h := newCallHarness(t, client)
	h.call()
	agent := h.answer()
	<-h.o.dialed
	h.readUntil(agent, msgAnswered)
	_ = agent.Close()
	_ = h.pres.Close()
	deadline := time.Now().Add(2 * time.Second)
	for h.srv.droppedAgentLeg(h.tenant.String(), "alice") == nil {
		if time.Now().After(deadline) {
			t.Fatal("agent's call slot never dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}

	pres := h.dial()
	h.send(pres, msg{Type: msgRegister})
	if m := h.readUntil(pres, msgResumeCall); !m.Phone || !m.Auto {
		t.Fatalf("resume-call %+v, want phone + auto (the agent's own outbound call)", m)
	}
}
