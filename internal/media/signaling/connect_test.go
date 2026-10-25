package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// connectLeg is one scripted Originate result: a live leg, or err.
type connectLeg struct {
	leg  *room.Leg
	done chan struct{} // close to hang this leg up from the far end
	err  error
}

func newConnectLeg(id string) connectLeg {
	done := make(chan struct{})
	return connectLeg{
		leg: &room.Leg{
			Source: &fakeOutboundSource{id: room.ParticipantID(id), stop: make(chan struct{})},
			Write:  func(*rtp.Packet) error { return nil },
			DTMF:   make(chan string),
			Done:   done,
		},
		done: done,
	}
}

// connectOriginator answers each Originate with the next scripted result. A
// result with block set waits until the ctx ends (a ring nobody answers).
type connectOriginator struct {
	mu      sync.Mutex
	results []connectLeg
	block   map[int]bool // Originate call index → ring until ctx ends
	n       int
	dialed  chan string
	hangups chan room.ParticipantID
}

func newConnectOriginator(results ...connectLeg) *connectOriginator {
	return &connectOriginator{results: results, block: map[int]bool{}, dialed: make(chan string, 4), hangups: make(chan room.ParticipantID, 4)}
}

func (o *connectOriginator) Originate(ctx context.Context, req room.OriginateRequest, _ func(room.Progress)) (*room.Leg, error) {
	o.mu.Lock()
	i := o.n
	o.n++
	o.mu.Unlock()
	o.dialed <- req.To
	if o.block[i] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	r := o.results[i]
	if r.err != nil {
		return nil, r.err
	}
	return r.leg, nil
}

func (o *connectOriginator) HangupOutbound(pid room.ParticipantID) error {
	o.hangups <- pid
	return nil
}

// connectCallLog records the connect call's CDR calls in order.
type connectCallLog struct {
	mu     sync.Mutex
	id     uuid.UUID
	events []string
	end    chan string // "failed:<cause>" / "ended:<cause>"
}

func newConnectCallLog() *connectCallLog {
	return &connectCallLog{id: uuid.New(), end: make(chan string, 1)}
}

func (c *connectCallLog) add(e string) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *connectCallLog) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

func (c *connectCallLog) Started(context.Context, uuid.UUID, string, string, string, string, string, room.ParticipantID) room.TrackSink {
	return nil
}
func (c *connectCallLog) StartedOutbound(_ context.Context, _ uuid.UUID, _, from, to, _ string, _ room.ParticipantID) room.TrackSink {
	c.add("started:" + from + ">" + to)
	return nil
}
func (c *connectCallLog) Answered(_ string, pid room.ParticipantID, _ string) room.TrackSink {
	c.add("answered:" + string(pid))
	return nil
}
func (c *connectCallLog) Customer(_ string, pid room.ParticipantID) room.TrackSink {
	c.add("customer:" + string(pid))
	return nil
}
func (c *connectCallLog) CallID(string) uuid.UUID                           { return c.id }
func (c *connectCallLog) RingStart(context.Context, string, string, string) {}
func (c *connectCallLog) RingAgent(string, string)                          {}
func (c *connectCallLog) RingStop(string)                                   {}
func (c *connectCallLog) Transferred(string, string, string)                {}
func (c *connectCallLog) Journey(string, []ivr.Step)                        {}
func (c *connectCallLog) Failed(_, cause string) {
	c.add("failed:" + cause)
	c.end <- "failed:" + cause
}
func (c *connectCallLog) Ended(_, cause string) { c.add("ended:" + cause); c.end <- "ended:" + cause }

func newConnectServer(t *testing.T, o *connectOriginator, cl CallLog) *Server {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("sfu.New: %v", err)
	}
	trunks := fakeTrunkResolver{trunks: []trunkselect.Trunk{{E164: "+912265915000", Name: "t_4cc0a6963ea0"}}}
	return New(Config{SFU: s, Originator: o, Trunks: trunks, Calls: cl, JWTSecret: []byte(testControlSecret)})
}

func waitEnd(t *testing.T, cl *connectCallLog) string {
	t.Helper()
	select {
	case e := <-cl.end:
		return e
	case <-time.After(2 * time.Second):
		t.Fatalf("call never settled; events %v", cl.snapshot())
		return ""
	}
}

func TestConnectBridgesAgentThenClient(t *testing.T) {
	agent, client := newConnectLeg("out-agent"), newConnectLeg("out-client")
	o := newConnectOriginator(agent, client)
	cl := newConnectCallLog()
	srv := newConnectServer(t, o, cl)

	id, err := srv.Connect(context.Background(), uuid.New(), "9876543210", "09812345678")
	if err != nil || id != cl.id {
		t.Fatalf("Connect = (%v, %v), want (%v, nil)", id, err, cl.id)
	}
	if got := <-o.dialed; got != "+919876543210" {
		t.Errorf("first dialed %q, want the agent +919876543210", got)
	}
	if got := <-o.dialed; got != "+919812345678" {
		t.Errorf("second dialed %q, want the client +919812345678", got)
	}

	// Connected: wait for the answer, then the client hangs up.
	deadline := time.Now().Add(2 * time.Second)
	for len(cl.snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(client.done)
	if got := waitEnd(t, cl); got != "ended:client_hangup" {
		t.Errorf("settled %q, want ended:client_hangup", got)
	}
	want := []string{"started:+919876543210>+919812345678", "customer:out-client", "answered:out-agent", "ended:client_hangup"}
	if got := cl.snapshot(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("cdr events %v, want %v", got, want)
	}
}

func TestConnectAgentNotAnswered(t *testing.T) {
	o := newConnectOriginator(connectLeg{err: errors.New("486 Busy Here")})
	cl := newConnectCallLog()
	srv := newConnectServer(t, o, cl)
	if _, err := srv.Connect(context.Background(), uuid.New(), "9876543210", "9812345678"); err != nil {
		t.Fatal(err)
	}
	if got := waitEnd(t, cl); got != "failed:agent_not_answered" {
		t.Errorf("settled %q, want failed:agent_not_answered", got)
	}
	select {
	case n := <-o.dialed:
		if n != "+919876543210" {
			t.Errorf("dialed %q", n)
		}
	default:
	}
	if len(o.dialed) != 0 {
		t.Error("the client was dialed although the agent never answered")
	}
}

func TestConnectClientNotAnsweredHangsUpAgent(t *testing.T) {
	agent := newConnectLeg("out-agent")
	o := newConnectOriginator(agent, connectLeg{err: errors.New("480 Temporarily Unavailable")})
	cl := newConnectCallLog()
	srv := newConnectServer(t, o, cl)
	if _, err := srv.Connect(context.Background(), uuid.New(), "9876543210", "9812345678"); err != nil {
		t.Fatal(err)
	}
	if got := waitEnd(t, cl); got != "failed:client_not_answered" {
		t.Errorf("settled %q, want failed:client_not_answered", got)
	}
	select {
	case pid := <-o.hangups:
		if pid != "out-agent" {
			t.Errorf("hung up %q, want the agent's leg", pid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent's leg was never hung up")
	}
}

func TestConnectAgentHangsUpWhileClientRings(t *testing.T) {
	agent := newConnectLeg("out-agent")
	o := newConnectOriginator(agent, connectLeg{})
	o.block[1] = true // the client rings until cancelled
	cl := newConnectCallLog()
	srv := newConnectServer(t, o, cl)
	if _, err := srv.Connect(context.Background(), uuid.New(), "9876543210", "9812345678"); err != nil {
		t.Fatal(err)
	}
	<-o.dialed
	<-o.dialed // client ringing
	close(agent.done)
	if got := waitEnd(t, cl); got != "failed:agent_hangup" {
		t.Errorf("settled %q, want failed:agent_hangup", got)
	}
}

func TestConnectRejectsBeforePlacing(t *testing.T) {
	o := newConnectOriginator()
	cl := newConnectCallLog()
	srv := newConnectServer(t, o, cl)
	if _, err := srv.Connect(context.Background(), uuid.New(), "12345", "9812345678"); !errors.Is(err, errDialInvalidNumber) {
		t.Errorf("bad agent number: err = %v, want errDialInvalidNumber", err)
	}
	srv.cfg.Trunks = fakeTrunkResolver{}
	if _, err := srv.Connect(context.Background(), uuid.New(), "9876543210", "9812345678"); !errors.Is(err, errDialNoTrunk) {
		t.Errorf("no trunk: err = %v, want errDialNoTrunk", err)
	}
	if got := cl.snapshot(); len(got) != 0 {
		t.Errorf("a call row was opened for a call never placed: %v", got)
	}
}

const testControlSecret = "control-test-secret-at-least-32-bytes!!"

func controlToken(t *testing.T, aud string, tenant uuid.UUID, exp time.Duration) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, controlClaims{
		TenantID: tenant,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Audience:  []string{aud},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(exp)),
		},
	}).SignedString([]byte(testControlSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestControlConnect(t *testing.T) {
	agent, client := newConnectLeg("out-agent"), newConnectLeg("out-client")
	o := newConnectOriginator(agent, client)
	cl := newConnectCallLog()
	h := newConnectServer(t, o, cl).ControlHandler()
	body := `{"agent_number":"9876543210","client_number":"9812345678"}`

	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/control/connect", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	tenant := uuid.New()
	for name, tok := range map[string]string{
		"no token":           "",
		"agent token":        controlToken(t, agentAudience, tenant, time.Minute),
		"expired":            controlToken(t, controlAudience, tenant, -time.Minute),
		"supervisor token":   controlToken(t, supervisorAudience, tenant, time.Minute),
		"visitor call token": controlToken(t, gatewayAudience, tenant, time.Minute),
	} {
		if rec := post(tok, body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
	tok := controlToken(t, controlAudience, tenant, time.Minute)
	if rec := post(tok, `{"agent_number":"1","client_number":"9812345678"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad number: status %d, want 400", rec.Code)
	}
	rec := post(tok, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	var out struct {
		CallID uuid.UUID `json:"call_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.CallID != cl.id {
		t.Errorf("body %s, want call_id %s", rec.Body, cl.id)
	}
	close(agent.done)
	waitEnd(t, cl)
}
