package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
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
