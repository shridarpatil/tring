package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func TestVersionReportsBuildVersion(t *testing.T) {
	f := dbtest.Setup(t)
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", Version: "v1.2.3"})

	rec := serve(t, s, f, "GET", "/v1/version", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["version"] != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", out["version"])
	}

	// Not advertised to unauthenticated callers.
	rec = httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/version", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token got %d, want 401", rec.Code)
	}
}
