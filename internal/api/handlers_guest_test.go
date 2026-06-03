package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whatomate/tring/internal/dbtest"
)

// testOrigin is the allowlisted origin the fixture tenant trusts; guest mint
// requests must carry it as their Origin header.
const testOrigin = "http://localhost:9999"

// seedFlowAndPublicKey gives the fixture tenant a public widget key, an
// active flow pinned as the support flow, and an Origin allowlist — the
// minimum a guest needs to mint a gateway call token. Returns (publicKey, flowID).
func seedFlowAndPublicKey(t *testing.T, f *dbtest.Fixture) (string, string) {
	t.Helper()
	ctx := context.Background()
	const def = `{"version":2,"entry_node":"n1","nodes":[{"id":"n1","type":"start"}],"edges":[]}`
	var flowID string
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO flows (tenant_id, name, definition, is_active)
		VALUES ($1, 'support', $2::jsonb, true) RETURNING id
	`, f.TenantID, def).Scan(&flowID); err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	const pk = "pk_live_guesttest"
	if _, err := f.DB.Pool.Exec(ctx, `
		UPDATE tenants SET public_key = $1, support_flow_id = $2, allowed_origins = $3 WHERE id = $4
	`, pk, flowID, []string{testOrigin}, f.TenantID); err != nil {
		t.Fatalf("set public key: %v", err)
	}
	return pk, flowID
}

// mintReq builds a guest-credentials POST with the given public key and
// Origin header. Pass origin="" to omit it.
func mintReq(pk, origin string) *http.Request {
	r := httptest.NewRequest("POST", "/v1/sdk/guest-credentials",
		strings.NewReader(`{"public_key":"`+pk+`"}`))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

// TestGuestCredentialsMintsGatewayToken is the headline test: a valid public
// key + allowed Origin yields the gateway URL, the pinned flow, and a verifiable
// gateway call token (anonymous → no client_ref). No SIP, no guest_sessions.
func TestGuestCredentialsMintsGatewayToken(t *testing.T) {
	f := dbtest.Setup(t)
	pk, flowID := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, mintReq(pk, testOrigin))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp guestCredentialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GatewayURL == "" {
		t.Error("gateway_url is empty")
	}
	if resp.FlowID != flowID {
		t.Errorf("flow_id = %q, want %q", resp.FlowID, flowID)
	}
	claims, err := s.ParseGatewayCallToken(resp.Token)
	if err != nil {
		t.Fatalf("returned token doesn't validate: %v", err)
	}
	if claims.FlowID != flowID {
		t.Errorf("token flow_id = %q, want %q", claims.FlowID, flowID)
	}
	if claims.ClientRef != "" {
		t.Errorf("public mint must not set client_ref, got %q", claims.ClientRef)
	}
}

// TestGuestCredentialsRejectsUnknownKey: a bogus public key is 401.
func TestGuestCredentialsRejectsUnknownKey(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, mintReq("pk_live_nope", testOrigin))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestGuestCredentialsRejectsDisallowedOrigin: valid key but an Origin not in
// the tenant's allowlist is 403.
func TestGuestCredentialsRejectsDisallowedOrigin(t *testing.T) {
	f := dbtest.Setup(t)
	pk, _ := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, mintReq(pk, "https://evil.example.com"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for disallowed origin", rec.Code)
	}
}

// TestGuestCredentialsRejectsMissingOrigin: fail-closed — no Origin header
// (non-browser caller) is 403 even with a valid key.
func TestGuestCredentialsRejectsMissingOrigin(t *testing.T) {
	f := dbtest.Setup(t)
	pk, _ := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, mintReq(pk, "")) // no Origin

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for missing origin", rec.Code)
	}
}

// TestGuestMintRateLimit: more than guestMintRatePerMin requests from one IP
// in the window get a 429 from the rate-limit middleware.
func TestGuestMintRateLimit(t *testing.T) {
	f := dbtest.Setup(t)
	pk, _ := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	var got429 bool
	for range guestMintRatePerMin + 1 {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, mintReq(pk, testOrigin))
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
		}
	}
	if !got429 {
		t.Fatalf("expected a 429 after >%d requests from one IP", guestMintRatePerMin)
	}
}
