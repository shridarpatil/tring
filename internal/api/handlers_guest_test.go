package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/whatomate/tring/internal/dbtest"
)

// testOrigin is the allowlisted origin the fixture tenant trusts; guest mint
// requests must carry it as their Origin header.
const testOrigin = "http://localhost:9999"

// seedFlowAndPublicKey gives the fixture tenant a public widget key, an
// active flow pinned as the support flow, and an Origin allowlist — the
// minimum a guest needs to mint credentials. Returns (publicKey, flowID).
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

// TestGuestCredentialsMintsFlowPinnedSession is the headline test: a valid
// public key + allowed Origin yields SIP creds AND persists a guest_sessions
// row pinned to the support flow with a future expiry (exercises make_interval).
func TestGuestCredentialsMintsFlowPinnedSession(t *testing.T) {
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
	if !strings.HasPrefix(resp.SipUsername, "guest_") {
		t.Errorf("sip_username = %q, want guest_ prefix", resp.SipUsername)
	}
	if resp.DialTarget != guestDialTarget {
		t.Errorf("dial_target = %q, want %q", resp.DialTarget, guestDialTarget)
	}

	var (
		gotFlow string
		future  bool
	)
	err := f.DB.Pool.QueryRow(context.Background(), `
		SELECT flow_id, expires_at > now() FROM guest_sessions WHERE sip_username = $1
	`, resp.SipUsername).Scan(&gotFlow, &future)
	if err != nil {
		t.Fatalf("guest_sessions lookup: %v", err)
	}
	if gotFlow != flowID {
		t.Errorf("session flow_id = %q, want %q", gotFlow, flowID)
	}
	if !future {
		t.Error("expires_at is not in the future — make_interval TTL wrong")
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
// the tenant's allowlist is 403, and no session is created.
func TestGuestCredentialsRejectsDisallowedOrigin(t *testing.T) {
	f := dbtest.Setup(t)
	pk, _ := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, mintReq(pk, "https://evil.example.com"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for disallowed origin", rec.Code)
	}
	var n int
	_ = f.DB.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM guest_sessions WHERE tenant_id = $1`, f.TenantID).Scan(&n)
	if n != 0 {
		t.Errorf("created %d sessions for a disallowed origin, want 0", n)
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

// TestGuestCredentialsConcurrentCap: once a tenant is at the live-session cap,
// further mints are rejected 429.
func TestGuestCredentialsConcurrentCap(t *testing.T) {
	f := dbtest.Setup(t)
	pk, flowID := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	// Fill the tenant to the cap with live sessions.
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO guest_sessions (tenant_id, sip_username, sip_password, flow_id, expires_at)
		SELECT $1, 'guest_cap_' || g, 'pw', $2, now() + interval '15 min'
		FROM generate_series(1, $3) g
	`, f.TenantID, flowID, maxConcurrentGuestSessions); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, mintReq(pk, testOrigin))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 at the concurrent cap; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGuestMintRateLimit: more than guestMintRatePerMin requests from one IP
// in the window get a 429 from the rate-limit middleware.
func TestGuestMintRateLimit(t *testing.T) {
	f := dbtest.Setup(t)
	pk, _ := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	var got429 bool
	for i := 0; i < guestMintRatePerMin+1; i++ {
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

// TestFSDirectoryGuestBranch: once a guest session exists, the mod_xml_curl
// directory lookup for that username returns user_class=guest and the
// pinned tring_flow_id — the variables the dialplan + executor rely on.
func TestFSDirectoryGuestBranch(t *testing.T) {
	f := dbtest.Setup(t)
	pk, flowID := seedFlowAndPublicKey(t, f)
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", FSBasicAuth: "fs:secret"})

	mintRec := httptest.NewRecorder()
	s.Router().ServeHTTP(mintRec, mintReq(pk, testOrigin))
	if mintRec.Code != http.StatusOK {
		t.Fatalf("mint status = %d", mintRec.Code)
	}
	var creds guestCredentialsResponse
	_ = json.Unmarshal(mintRec.Body.Bytes(), &creds)

	form := url.Values{
		"section": {"directory"},
		"user":    {creds.SipUsername},
		"domain":  {f.SIPDomain},
	}
	dir := httptest.NewRequest("POST", "/fs/directory", strings.NewReader(form.Encode()))
	dir.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dir.SetBasicAuth("fs", "secret")
	dirRec := httptest.NewRecorder()
	s.Router().ServeHTTP(dirRec, dir)

	if dirRec.Code != http.StatusOK {
		t.Fatalf("directory status = %d; body=%s", dirRec.Code, dirRec.Body.String())
	}
	body := dirRec.Body.String()
	for _, want := range []string{
		`name="user_class" value="guest"`,
		`name="tring_flow_id" value="` + flowID + `"`,
		`name="password"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("directory XML missing %q\n--- body ---\n%s", want, body)
		}
	}
}
