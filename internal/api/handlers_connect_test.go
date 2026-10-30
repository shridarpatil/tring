package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// TestConnectCallViaGateway: POST /v1/calls/connect hands the call to the
// gateway's control endpoint with a tenant-scoped control token and answers
// with the call row the gateway opened; the gateway's 4xx pass through and an
// unreachable gateway is a 502.
func TestConnectCallViaGateway(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	var callID uuid.UUID
	if err := f.DB.Pool.QueryRow(ctx, `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, source)
		VALUES ($1, 'outbound', 'ringing', '+919876543210', '+919812345678', 'leg-connect', 'phone') RETURNING id`, f.TenantID).Scan(&callID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := newTestServer(f)
	var gotTenant uuid.UUID
	var gotBody connectCallRequest
	status := http.StatusCreated
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c ControlClaims
		raw := r.Header.Get("Authorization")[len("Bearer "):]
		if _, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return s.jwtSecret, nil },
			jwt.WithAudience(controlAudience), jwt.WithIssuer(jwtIssuer), jwt.WithExpirationRequired()); err != nil {
			t.Errorf("control token: %v", err)
		}
		gotTenant = c.TenantID
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(map[string]uuid.UUID{"call_id": callID})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent_number: bad format"})
		}
	}))
	defer gw.Close()
	s.gatewayControlURL = gw.URL

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/calls/connect",
			`{"agent_number":"9876543210","client_number":"9812345678"}`))
		return rec
	}

	rec := post()
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	var c Call
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.ID != callID || c.Status != "ringing" {
		t.Errorf("body %s, want call %s ringing", rec.Body, callID)
	}
	if gotTenant != f.TenantID {
		t.Errorf("control token tenant %s, want %s", gotTenant, f.TenantID)
	}
	if gotBody.AgentNumber != "9876543210" || gotBody.ClientNumber != "9812345678" {
		t.Errorf("gateway got %+v", gotBody)
	}

	status = http.StatusBadRequest
	if rec := post(); rec.Code != http.StatusBadRequest {
		t.Errorf("gateway 400: status %d, want 400", rec.Code)
	}

	gw.Close()
	if rec := post(); rec.Code != http.StatusBadGateway {
		t.Errorf("gateway down: status %d, want 502", rec.Code)
	}
}

// TestCreateCallViaGateway: POST /v1/calls resolves agent_id (UUID or
// sip_username) to the agent's SIP and hands the call to the gateway's
// /control/call; the gateway's 409 (agent offline) passes through, and an
// unknown agent is a 404 that never reaches the gateway.
func TestCreateCallViaGateway(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	var agentID, callID uuid.UUID
	if err := f.DB.Pool.QueryRow(ctx, `INSERT INTO agents (tenant_id, sip_username, sip_password)
		VALUES ($1, 'Administrator', 'agent-pw') RETURNING id`, f.TenantID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, source)
		VALUES ($1, 'outbound', 'ringing', '+912265915000', '+919812345678', 'leg-call', 'phone') RETURNING id`, f.TenantID).Scan(&callID); err != nil {
		t.Fatalf("seed call: %v", err)
	}

	s := newTestServer(f)
	var gotPath string
	var gotBody map[string]string
	status := http.StatusCreated
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(map[string]uuid.UUID{"call_id": callID})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent is not connected"})
		}
	}))
	defer gw.Close()
	s.gatewayControlURL = gw.URL

	post := func(agent string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/calls",
			`{"agent_id":"`+agent+`","to":"9812345678","agent_label":"Lead 42","caller_id":"+912265915002"}`))
		return rec
	}

	for _, agent := range []string{agentID.String(), "Administrator"} {
		gotBody = nil
		rec := post(agent)
		if rec.Code != http.StatusCreated {
			t.Fatalf("agent_id %q: status %d, want 201: %s", agent, rec.Code, rec.Body)
		}
		var c Call
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.ID != callID {
			t.Errorf("agent_id %q: body %s, want call %s", agent, rec.Body, callID)
		}
		want := map[string]string{"agent_sip": "Administrator", "to": "9812345678", "agent_label": "Lead 42", "caller_id": "+912265915002"}
		if gotPath != "/control/call" || len(gotBody) != len(want) {
			t.Errorf("agent_id %q: gateway got %s %v, want /control/call %v", agent, gotPath, gotBody, want)
		}
		for k, v := range want {
			if gotBody[k] != v {
				t.Errorf("agent_id %q: gateway %s = %q, want %q", agent, k, gotBody[k], v)
			}
		}
	}

	gotPath = ""
	if rec := post("nobody"); rec.Code != http.StatusNotFound || gotPath != "" {
		t.Errorf("unknown agent: status %d (gateway hit: %q), want 404 without a gateway call", rec.Code, gotPath)
	}

	status = http.StatusConflict
	if rec := post("Administrator"); rec.Code != http.StatusConflict {
		t.Errorf("agent offline: status %d, want 409", rec.Code)
	}
}
