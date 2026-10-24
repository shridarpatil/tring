package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/secretbox"
)

// ssrfWebhookURLs must all be refused at create time (SSRF: ESL, cloud
// metadata, loopback by name, non-http scheme).
var ssrfWebhookURLs = []string{
	"http://127.0.0.1:8021",
	"http://169.254.169.254/",
	"http://localhost/",
	"ftp://x",
}

// The URL check runs before any DB access, so it's testable without a DB.
func TestCreateWebhookRejectsInternalURLsNoDB(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret"})
	for _, u := range ssrfWebhookURLs {
		r := httptest.NewRequest("POST", "/v1/webhooks", strings.NewReader(`{"url":"`+u+`"}`))
		r = r.WithContext(context.WithValue(r.Context(), tenantCtxKey, &Tenant{ID: uuid.New()}))
		rec := httptest.NewRecorder()
		s.handleCreateWebhook(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %q: status %d, want 400", u, rec.Code)
		}
	}
}

// Same, end to end through the authed router.
func TestCreateWebhookRejectsInternalURLs(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	for _, u := range ssrfWebhookURLs {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/webhooks", `{"url":"`+u+`"}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %q: status %d, want 400 (body %s)", u, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, "POST", "/v1/webhooks", `{"url":"https://example.com/hook"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("public URL: status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestValidateWebhookHeaders(t *testing.T) {
	bad := []map[string]string{
		{"Bad Name": "x"},
		{"Content-Type": "text/plain"},
		{"tring-event": "spoofed"},
		{"X-Token": "a\r\nInjected: yes"},
		{"": "x"},
	}
	for _, h := range bad {
		if validateWebhookHeaders(h) == nil {
			t.Errorf("headers %v accepted, want rejected", h)
		}
	}
	if err := validateWebhookHeaders(map[string]string{"Authorization": "Bearer x", "X-Api-Key": "k"}); err != nil {
		t.Errorf("valid headers rejected: %v", err)
	}
}

// Create → edit → disable → delete. Header values are sealed at rest and never
// returned; only their names are.
func TestWebhookRegistryLifecycle(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSealingServer(f, testBox(t, 7))
	do := func(method, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, method, target, body))
		return rec
	}

	rec := do("POST", "/v1/webhooks", `{"name":"CRM","url":"https://example.com/hook","headers":{"Authorization":"Bearer crm-secret"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var h Webhook
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if h.Name != "CRM" || !h.Active || h.Secret == "" || len(h.HeaderNames) != 1 || h.HeaderNames[0] != "Authorization" {
		t.Fatalf("created = %+v, want CRM, active, a secret, header Authorization", h)
	}
	if strings.Contains(rec.Body.String(), "crm-secret") {
		t.Fatal("a header value was returned")
	}
	var stored string
	if err := f.DB.Pool.QueryRow(context.Background(), `SELECT headers FROM webhooks WHERE id = $1`, h.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(stored) {
		t.Fatalf("headers stored as %q, want sealed", stored)
	}

	rec = do("PATCH", "/v1/webhooks/"+h.ID.String(), `{"active":false,"name":"CRM prod"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	var patched Webhook
	_ = json.Unmarshal(rec.Body.Bytes(), &patched)
	if patched.Active || patched.Name != "CRM prod" || len(patched.HeaderNames) != 1 || patched.Secret != "" {
		t.Fatalf("patched = %+v, want disabled, renamed, headers kept, no secret", patched)
	}
	rec = do("PATCH", "/v1/webhooks/"+h.ID.String(), `{"headers":{}}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &patched)
	if len(patched.HeaderNames) != 0 {
		t.Fatalf("headers after clearing = %v, want none", patched.HeaderNames)
	}
	if rec := do("PATCH", "/v1/webhooks/"+h.ID.String(), `{"url":"http://169.254.169.254/"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch to an internal URL: %d, want 400", rec.Code)
	}

	rec = do("GET", "/v1/webhooks", "")
	var list []Webhook
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != h.ID || list[0].Secret != "" {
		t.Fatalf("list = %+v", list)
	}
	if rec := do("DELETE", "/v1/webhooks/"+h.ID.String(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
}

// Another tenant's webhook can't be edited or deleted.
func TestWebhookCrossTenant(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	other, _, _ := otherTenant(t, f)
	var id uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(),
		`INSERT INTO webhooks (tenant_id, name, url, secret) VALUES ($1, 'theirs', 'https://example.com', 's') RETURNING id`,
		other).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ method, body string }{{"PATCH", `{"active":false}`}, {"DELETE", ""}} {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, m.method, "/v1/webhooks/"+id.String(), m.body))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s another tenant's webhook: %d, want 404", m.method, rec.Code)
		}
	}
}
