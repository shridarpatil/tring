package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// fakeGatewayControl records control requests and answers each with status.
type fakeGatewayControl struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]string
	status int
	errMsg string
}

func (g *fakeGatewayControl) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.paths = append(g.paths, r.Method+" "+r.URL.Path)
		g.bodies = append(g.bodies, body)
		status, msg := g.status, g.errMsg
		g.mu.Unlock()
		if status >= 300 {
			controlErrorBody(w, status, msg)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func controlErrorBody(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// The agent-token transfer endpoint used to send uuid_setvar/uuid_transfer to
// FreeSWITCH for calls.leg_id, which is a random id for every media-plane call,
// so it always failed with 502. It must hand the call's room to the gateway.
func TestSdkTransferGoesThroughGateway(t *testing.T) {
	f := dbtest.Setup(t)
	gw := &fakeGatewayControl{status: http.StatusNoContent}
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test", GatewayControlURL: gw.start(t)})
	me := f.AddUser(t, "agent", "me@example.com")
	roomID := f.TenantID.String() + "/room-1"
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, source, room_id, agent_id, answered_at)
		VALUES ($1, 'inbound', 'answered', '+919800000001', 'Support', $2, 'web', $3, $4, now())`,
		f.TenantID, uuid.NewString(), roomID, me); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.IssueAgentToken(me, f.TenantID, "me@example.com", f.SIPDomain, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, s.Router(), "POST", "/v1/sdk/calls/transfer", tok, `{"to_team":"sales"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("transfer: %d %s, want 200", rec.Code, rec.Body)
	}
	gw.mu.Lock()
	if len(gw.paths) != 1 || gw.paths[0] != "POST /control/transfer" {
		t.Fatalf("gateway saw %v, want one POST /control/transfer", gw.paths)
	}
	if b := gw.bodies[0]; b["room"] != roomID || b["sip"] != "me@example.com" || b["to_team"] != "sales" {
		t.Fatalf("gateway body = %v", b)
	}
	gw.mu.Unlock()

	// The gateway's refusals map through: busy target → 409, gone room → 404.
	for _, tc := range []struct{ gw, want int }{
		{http.StatusConflict, http.StatusConflict},
		{http.StatusNotFound, http.StatusNotFound},
		{http.StatusInternalServerError, http.StatusBadGateway},
	} {
		gw.mu.Lock()
		gw.status, gw.errMsg = tc.gw, "nope"
		gw.mu.Unlock()
		if rec := doReq(t, s.Router(), "POST", "/v1/sdk/calls/transfer", tok, `{"to_sip":"bob"}`); rec.Code != tc.want {
			t.Errorf("gateway %d: got %d %s, want %d", tc.gw, rec.Code, rec.Body, tc.want)
		}
	}

	// No answered call for this agent → 409 without reaching the gateway.
	other := f.AddUser(t, "agent", "other@example.com")
	otok, _, _ := s.IssueAgentToken(other, f.TenantID, "other@example.com", f.SIPDomain, time.Hour)
	if rec := doReq(t, s.Router(), "POST", "/v1/sdk/calls/transfer", otok, `{"to_team":"sales"}`); rec.Code != http.StatusConflict {
		t.Errorf("no active call: %d %s, want 409", rec.Code, rec.Body)
	}
}
