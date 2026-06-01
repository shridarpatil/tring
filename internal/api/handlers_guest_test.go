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

// seedFlowAndPublicKey gives the fixture tenant a public widget key, an
// active flow, and pins that flow as the tenant's support flow — the
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
		UPDATE tenants SET public_key = $1, support_flow_id = $2 WHERE id = $3
	`, pk, flowID, f.TenantID); err != nil {
		t.Fatalf("set public key: %v", err)
	}
	return pk, flowID
}

// TestGuestCredentialsMintsFlowPinnedSession is the headline test: a valid
// public key yields SIP creds AND persists a guest_sessions row pinned to
// the tenant's support flow with a future expiry (exercises make_interval).
func TestGuestCredentialsMintsFlowPinnedSession(t *testing.T) {
	f := dbtest.Setup(t)
	pk, flowID := seedFlowAndPublicKey(t, f)
	s := newTestServer(f)

	req := httptest.NewRequest("POST", "/v1/sdk/guest-credentials",
		strings.NewReader(`{"public_key":"`+pk+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

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

	// The session must exist, be pinned to the support flow, and not be
	// already expired.
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

// TestGuestCredentialsRejectsUnknownKey: a bogus public key is 401, and no
// session is created.
func TestGuestCredentialsRejectsUnknownKey(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	req := httptest.NewRequest("POST", "/v1/sdk/guest-credentials",
		strings.NewReader(`{"public_key":"pk_live_nope"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestFSDirectoryGuestBranch: once a guest session exists, the mod_xml_curl
// directory lookup for that username returns user_class=guest and the
// pinned tring_flow_id — the variables the dialplan + executor rely on.
func TestFSDirectoryGuestBranch(t *testing.T) {
	f := dbtest.Setup(t)
	pk, flowID := seedFlowAndPublicKey(t, f)
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", FSBasicAuth: "fs:secret"})

	// Mint a guest session first.
	mint := httptest.NewRequest("POST", "/v1/sdk/guest-credentials",
		strings.NewReader(`{"public_key":"`+pk+`"}`))
	mint.Header.Set("Content-Type", "application/json")
	mintRec := httptest.NewRecorder()
	s.Router().ServeHTTP(mintRec, mint)
	if mintRec.Code != http.StatusOK {
		t.Fatalf("mint status = %d", mintRec.Code)
	}
	var creds guestCredentialsResponse
	_ = json.Unmarshal(mintRec.Body.Bytes(), &creds)

	// Now emulate FreeSWITCH's directory POST for that user.
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
