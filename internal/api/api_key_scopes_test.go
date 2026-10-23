package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// A 'calls' key reaches the call/agent surface but none of the tenant admin
// routes; a 'full' key (the default, and every pre-existing key) reaches both.
func TestAPIKeyScopes(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	s := newTestServer(f)

	mint := func(scope string) apiKeyResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/api-keys", `{"name":"k","scope":"`+scope+`"}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %q key: %d %s", scope, rec.Code, rec.Body.String())
		}
		var k apiKeyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
			t.Fatal(err)
		}
		if k.Scope != scope {
			t.Fatalf("scope = %q, want %q", k.Scope, scope)
		}
		return k
	}
	call := func(key, method, target, body string) int {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, target, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		} else {
			r = httptest.NewRequest(method, target, nil)
		}
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, r)
		return rec.Code
	}

	calls := mint("calls").Key
	full := mint("full").Key

	admin := [][3]string{
		{"POST", "/v1/api-keys", `{"name":"x"}`},
		{"GET", "/v1/api-keys", ""},
		{"GET", "/v1/trunks", ""},
		{"POST", "/v1/webhooks", `{"url":"https://example.com/h"}`},
		{"PATCH", "/v1/tenants/me", `{"record_calls":true}`},
		{"POST", "/v1/flows", `{"name":"x"}`},
		{"POST", "/v1/teams", `{"name":"x"}`},
		{"GET", "/v1/audio", ""},
	}
	for _, c := range admin {
		if got := call(calls, c[0], c[1], c[2]); got != http.StatusForbidden {
			t.Errorf("calls key %s %s = %d, want 403", c[0], c[1], got)
		}
	}
	for _, c := range [][3]string{{"GET", "/v1/api-keys", ""}, {"GET", "/v1/trunks", ""}} {
		if got := call(full, c[0], c[1], c[2]); got != http.StatusOK {
			t.Errorf("full key %s %s = %d, want 200", c[0], c[1], got)
		}
	}
	for _, c := range [][3]string{
		{"GET", "/v1/calls", ""},
		{"GET", "/v1/agents", ""},
		{"GET", "/v1/flows", ""},
		{"POST", "/v1/agents", `{"sip_username":"crm-42"}`},
	} {
		if got := call(calls, c[0], c[1], c[2]); got >= 400 {
			t.Errorf("calls key %s %s = %d, want success", c[0], c[1], got)
		}
	}

	// Default scope is full; unknown scopes are rejected.
	if k := createKey(t, s, f, "default"); k.Scope != scopeFull {
		t.Errorf("default scope = %q, want full", k.Scope)
	}
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/api-keys", `{"scope":"admin"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown scope = %d, want 400", rec.Code)
	}
}
