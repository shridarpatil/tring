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
