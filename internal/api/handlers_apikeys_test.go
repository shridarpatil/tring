package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// createKey issues an API key via the authed endpoint and returns the parsed
// response (including the one-time plaintext).
func createKey(t *testing.T, s *Server, f *dbtest.Fixture, name string) apiKeyResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/api-keys", `{"name":"`+name+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: status %d, body %s", rec.Code, rec.Body.String())
	}
	var k apiKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return k
}

// Create issues a usable tk_live_ key (shown once); list reflects it without
// leaking the secret; revoke flips its status.
func TestAPIKeyCreateListRevoke(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	k := createKey(t, s, f, "backend")
	if !strings.HasPrefix(k.Key, "tk_live_") {
		t.Fatalf("key = %q, want tk_live_ prefix", k.Key)
	}

	// List: present, active, and the plaintext is NOT returned.
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "GET", "/v1/api-keys", ""))
	var list []apiKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].ID != k.ID || list[0].Revoked || list[0].Key != "" {
		t.Fatalf("list = %+v, want one active key with no plaintext", list)
	}

	// Revoke → 204, then list shows it revoked.
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "DELETE", "/v1/api-keys/"+k.ID.String(), ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "GET", "/v1/api-keys", ""))
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || !list[0].Revoked {
		t.Fatalf("after revoke, list = %+v, want revoked", list)
	}
}

// A live key authenticates a tenant-scoped request via AuthRequired; once
// revoked it's rejected.
func TestAPIKeyAuthenticatesAndRevokes(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	k := createKey(t, s, f, "backend")

	keyReq := func() *http.Request {
		r := httptest.NewRequest("GET", "/v1/api-keys", nil)
		r.Header.Set("Authorization", "Bearer "+k.Key)
		return r
	}

	// Works with the API key (not a console JWT).
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, keyReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("api-key auth: status %d, want 200", rec.Code)
	}

	// Revoke it, then the same key is rejected.
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "DELETE", "/v1/api-keys/"+k.ID.String(), ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, keyReq())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key: status %d, want 401", rec.Code)
	}
}
