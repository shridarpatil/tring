package signaling

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/google/uuid"
)

// A kick (the api renamed the agent's SIP username) closes their presence
// socket so the SDK reconnects under the new name; it touches only the named
// agent in the token's tenant.
func TestControlAgentKick(t *testing.T) {
	h := newCallHarness(t)
	ctl := h.srv.ControlHandler()
	post := func(tenant uuid.UUID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/control/agent-kick", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+controlToken(t, controlAudience, tenant, time.Minute))
		rec := httptest.NewRecorder()
		ctl.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(h.tenant, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no sip: status %d, want 400", rec.Code)
	}
	if rec := post(h.tenant, `{"sip":"bob"}`); rec.Code != http.StatusNoContent {
		t.Errorf("unknown agent: status %d, want 204", rec.Code)
	}
	if rec := post(uuid.New(), `{"sip":"alice"}`); rec.Code != http.StatusNoContent {
		t.Errorf("other tenant: status %d, want 204", rec.Code)
	}
	if !registered(h.srv, h.tenant.String(), "alice") {
		t.Fatal("alice kicked by a request for another tenant/agent")
	}

	if rec := post(h.tenant, `{"sip":"alice"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("kick: status %d, want 204", rec.Code)
	}
	_ = h.pres.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, err := wsutil.ReadServerText(h.pres); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				t.Fatal("presence socket still open after kick")
			}
			break
		}
	}
	deadline := time.Now().Add(time.Second)
	for registered(h.srv, h.tenant.String(), "alice") {
		if time.Now().After(deadline) {
			t.Fatal("alice still registered after kick")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func registered(s *Server, tenant, sip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[tenant][sip] != nil
}
