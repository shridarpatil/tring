package signaling

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// POST /control/transfer is the API's REST blind transfer: it runs the same
// transfer as the agent's msgTransfer, for the agent's leg in the named room,
// and only in the token's tenant.
func TestControlTransfer(t *testing.T) {
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{SFU: sf, JWTSecret: []byte(testControlSecret)})
	tenant := uuid.New()
	rid := room.RoomID(tenant.String() + "/xfer")
	caller, err := s.rooms.Join(rid, "caller")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.rooms.Join(rid, "agent")
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeConn{}
	s.mu.Lock()
	s.conns[agent.ID()] = fc.conn(agent.ID())
	s.agentLegs[agent.ID()] = agentLeg{tenant: tenant.String(), sip: "alice", p: agent}
	s.mu.Unlock()

	ctl := s.ControlHandler()
	post := func(tn uuid.UUID, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/control/transfer", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+controlToken(t, controlAudience, tn, time.Minute))
		rec := httptest.NewRecorder()
		ctl.ServeHTTP(rec, req)
		return rec.Code
	}
	body := `{"room":"` + string(rid) + `","sip":"alice","to_sip":"bob"}`

	if code := post(uuid.New(), body); code != http.StatusNotFound {
		t.Errorf("other tenant: %d, want 404", code)
	}
	if code := post(tenant, `{"room":"`+tenant.String()+`/nope","sip":"alice","to_sip":"bob"}`); code != http.StatusNotFound {
		t.Errorf("unknown room: %d, want 404", code)
	}
	if code := post(tenant, body); code != http.StatusConflict {
		t.Errorf("bob offline: %d, want 409", code)
	}
	if code := post(tenant, `{"room":"`+string(rid)+`","sip":"alice","to_sip":"alice"}`); code != http.StatusBadRequest {
		t.Errorf("self transfer: %d, want 400", code)
	}
	if fc.saw(msgEnded) {
		t.Fatal("a refused transfer dropped the agent")
	}

	target := &fakeConn{}
	s.mu.Lock()
	s.agents[tenant.String()] = map[string]*conn{"bob": target.conn("bob-pid")}
	s.mu.Unlock()
	if code := post(tenant, body); code != http.StatusNoContent {
		t.Fatalf("transfer: %d, want 204", code)
	}
	if !waitSaw(t, fc, msgEnded) {
		t.Fatal("agent was not sent a terminal `ended`")
	}
	waitGone(t, s, rid, agent.ID())
	if !s.transferActive(rid) {
		t.Fatal("target is not ringing")
	}
	caller.Leave() // unblock the ring goroutine
}
