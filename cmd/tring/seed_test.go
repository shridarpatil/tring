package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/shridarpatil/tring/internal/api"
	"github.com/shridarpatil/tring/internal/dbtest"
)

// The key --seed-tenant prints must authenticate. It used to be stored only on
// the legacy tenants.api_key_hash column while auth resolves keys from
// api_keys, so every request with it was a 401.
func TestSeededKeyAuthenticates(t *testing.T) {
	f := dbtest.Setup(t)
	var out bytes.Buffer
	if err := seedTenant(context.Background(), f.DB, "Seed Co", &out); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Bearer (\S+)`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no key printed:\n%s", out.String())
	}

	s := api.NewServer(api.Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test"})
	req := httptest.NewRequest(http.MethodGet, "/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+m[1])
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seeded key: status %d %s, want 200 (full scope)", rec.Code, rec.Body)
	}
}
