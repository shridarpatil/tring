package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// callHarness is a gateway with one tenant agent ("alice") whose presence socket
// is registered, as the widget's is.
type callHarness struct {
	t      *testing.T
	srv    *Server
	url    string
	tenant uuid.UUID
	o      *connectOriginator
	cl     *connectCallLog
	pres   net.Conn
}

func newCallHarness(t *testing.T, results ...connectLeg) *callHarness {
	t.Helper()
	h := &callHarness{t: t, tenant: uuid.New(), o: newConnectOriginator(results...), cl: newConnectCallLog()}
	h.srv = newConnectServer(t, h.o, h.cl)
	ts := httptest.NewServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	h.url = strings.Replace(ts.URL, "http://", "ws://", 1)
	h.pres = h.dial()
	h.send(h.pres, msg{Type: msgRegister})
	if m := h.read(h.pres); m.Type != msgReady {
		t.Fatalf("presence: got %+v, want ready", m)
	}
	return h
}

func (h *callHarness) dial() net.Conn {
	h.t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant": h.tenant.String(), "sip_user": "alice",
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

func (h *callHarness) send(c net.Conn, m msg) {
	h.t.Helper()
	b, _ := json.Marshal(m)
	if err := wsutil.WriteClientMessage(c, ws.OpText, b); err != nil {
		h.t.Fatalf("ws write: %v", err)
	}
}

func (h *callHarness) read(c net.Conn) msg {
	h.t.Helper()
	return readMsg(h.t, c)
}

// readUntil skips messages (offers, peer events) until one of type typ.
func (h *callHarness) readUntil(c net.Conn, typ string) msg {
	h.t.Helper()
	for {
		if m := h.read(c); m.Type == typ {
			return m
		}
	}
}

// answer does what the widget does on an auto incoming: join the room.
func (h *callHarness) answer() net.Conn {
	h.t.Helper()
	in := h.readUntil(h.pres, msgIncoming)
	if !in.Auto || in.From != "Lead 42" {
		h.t.Fatalf("incoming %+v, want auto with the label", in)
	}
	c := h.dial()
	h.send(c, msg{Type: msgJoin, Room: in.Room})
	h.readUntil(c, msgJoined)
	return c
}

func (h *callHarness) call() uuid.UUID {
	h.t.Helper()
	id, err := h.srv.Call(context.Background(), h.tenant, CallRequest{AgentSIP: "alice", To: "09812345678", Label: "Lead 42"})
	if err != nil {
		h.t.Fatalf("Call: %v", err)
	}
	return id
}

func TestCallRingsAgentThenCustomer(t *testing.T) {
	client := newConnectLeg("out-client")
	h := newCallHarness(t, client)
	if id := h.call(); id != h.cl.id {
		t.Fatalf("call id %v, want %v", id, h.cl.id)
	}
	h.answer()
	if got := <-h.o.dialed; got != "+919812345678" {
		t.Errorf("dialed %q, want the customer", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(h.cl.snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(client.done)
	if got := waitEnd(t, h.cl); got != "ended:client_hangup" {
		t.Errorf("settled %q, want ended:client_hangup", got)
	}
	// Answered only once the customer picked up, not on the agent's join.
	want := []string{"started:+912265915000>+919812345678", "customer:out-client"}
	got := h.cl.snapshot()
	if len(got) != 4 || strings.Join(got[:2], ",") != strings.Join(want, ",") || !strings.HasPrefix(got[2], "answered:") {
		t.Errorf("cdr events %v, want %v then answered, ended", got, want)
	}
}

func TestCallCustomerNotAnsweredEndsAgent(t *testing.T) {
	h := newCallHarness(t, connectLeg{err: errors.New("480 Temporarily Unavailable")})
	h.call()
	agent := h.answer()
	if got := waitEnd(t, h.cl); got != "failed:client_not_answered" {
		t.Errorf("settled %q, want failed:client_not_answered", got)
	}
	h.readUntil(agent, msgEnded)
	for _, e := range h.cl.snapshot() {
		if strings.HasPrefix(e, "answered:") {
			t.Errorf("a call that never connected was marked answered: %v", h.cl.snapshot())
		}
	}
}

func TestCallAgentHangsUpWhileCustomerRings(t *testing.T) {
	h := newCallHarness(t, connectLeg{})
	h.o.block[0] = true // the customer rings until cancelled
	h.call()
	agent := h.answer()
	<-h.o.dialed
	h.send(agent, msg{Type: msgBye})
	if got := waitEnd(t, h.cl); got != "failed:agent_hangup" {
		t.Errorf("settled %q, want failed:agent_hangup", got)
	}
}

func TestCallAgentNeverAnswers(t *testing.T) {
	old := callAgentTimeout
	callAgentTimeout = 100 * time.Millisecond
	t.Cleanup(func() { callAgentTimeout = old })

	h := newCallHarness(t)
	h.call()
	h.readUntil(h.pres, msgIncoming)
	if got := waitEnd(t, h.cl); got != "failed:agent_not_answered" {
		t.Errorf("settled %q, want failed:agent_not_answered", got)
	}
	h.readUntil(h.pres, msgCancelRing)
	if len(h.o.dialed) != 0 {
		t.Error("the customer was dialed although the agent never joined")
	}
}

func TestCallRejectsBeforePlacing(t *testing.T) {
	h := newCallHarness(t)
	for name, tc := range map[string]struct {
		req  CallRequest
		want error
	}{
		"offline agent":  {CallRequest{AgentSIP: "bob", To: "9812345678"}, errAgentOffline},
		"bad number":     {CallRequest{AgentSIP: "alice", To: "12345"}, errDialInvalidNumber},
		"unowned callid": {CallRequest{AgentSIP: "alice", To: "9812345678", CallerID: "+912200000000"}, errDialCallerID},
	} {
		if _, err := h.srv.Call(context.Background(), h.tenant, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if got := h.cl.snapshot(); len(got) != 0 {
		t.Errorf("a call row was opened for a call never placed: %v", got)
	}
}

func TestControlCall(t *testing.T) {
	h := newCallHarness(t)
	ctl := h.srv.ControlHandler()
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/control/call", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+controlToken(t, controlAudience, h.tenant, time.Minute))
		rec := httptest.NewRecorder()
		ctl.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"agent_sip":"bob","to":"9812345678"}`); rec.Code != http.StatusConflict {
		t.Errorf("offline agent: status %d, want 409", rec.Code)
	}
	if rec := post(`{"to":"9812345678"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no agent: status %d, want 400", rec.Code)
	}
	rec := post(`{"agent_sip":"alice","to":"9812345678","agent_label":"Lead 42"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	var out struct {
		CallID uuid.UUID `json:"call_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.CallID != h.cl.id {
		t.Errorf("body %s, want call_id %s", rec.Body, h.cl.id)
	}
}
