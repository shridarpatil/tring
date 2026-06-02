package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSDKWidgetServed verifies the embedded browser bundles are served at
// /sdk/<name>.js so a self-hoster's embed snippet works with no CDN. Needs
// no DB — but the bundles must be built first (`make sdk` / the Dockerfile
// sdk stage). CI runs `make sdk` before `go test`; locally, run it once.
func TestSDKWidgetServed(t *testing.T) {
	s := NewServer(Config{JWTSecret: "test-secret"})
	r := s.Router()

	for _, name := range []string{"voice.js", "agent.js", "tring.js"} {
		req := httptest.NewRequest("GET", "/sdk/"+name, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("GET /sdk/%s = %d, want 200 (did you run `make sdk`?)", name, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("/sdk/%s Content-Type = %q, want javascript", name, ct)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("/sdk/%s served an empty body", name)
		}
	}
}
