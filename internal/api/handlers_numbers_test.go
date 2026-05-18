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

// numberRow returns the (id, active) of the inserted number so the test
// can target it in PATCH calls.
func insertOutboundForHandlerTest(t *testing.T, f *dbtest.Fixture, e164 string, active bool) string {
	t.Helper()
	var id string
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, priority, active)
		VALUES ($1, $2, 'outbound', 'exotel', 100, $3)
		RETURNING id
	`, f.TenantID, e164, active).Scan(&id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

// newTestServer builds an *api.Server wired to the fixture DB. The FS
// client and notifier fields stay nil — the handlers under test in this
// file don't call them.
func newTestServer(f *dbtest.Fixture) http.Handler {
	s := NewServer(Config{
		DB:        f.DB,
		JWTSecret: "test-secret",
	})
	return s.Router()
}

// TestPatchNumberLastActiveGuardrail is the headline test for the
// invariant we verified live during the Tring build:
// deactivating the only active outbound trunk must return 409.
func TestPatchNumberLastActiveGuardrail(t *testing.T) {
	f := dbtest.Setup(t)
	id := insertOutboundForHandlerTest(t, f, "+918001234567", true)
	srv := newTestServer(f)

	req := httptest.NewRequest("PATCH", "/v1/numbers/"+id, strings.NewReader(`{"active": false}`))
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 — deactivating last active trunk must be rejected", rec.Code)
		t.Logf("body: %s", rec.Body.String())
	}
}

// With a second active trunk in place, deactivating one of them succeeds.
func TestPatchNumberDeactivateWithSpareSucceeds(t *testing.T) {
	f := dbtest.Setup(t)
	primary := insertOutboundForHandlerTest(t, f, "+918001234567", true)
	insertOutboundForHandlerTest(t, f, "+918007654321", true) // the spare
	srv := newTestServer(f)

	req := httptest.NewRequest("PATCH", "/v1/numbers/"+primary, strings.NewReader(`{"active": false}`))
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["active"] != false {
		t.Errorf("returned row should be active=false, got %v", out["active"])
	}
}

// TestAuthRequiredRejectsMissingKey covers the API-key auth wall — the
// gateway every tenant-scoped request flows through.
func TestAuthRequiredRejectsMissingKey(t *testing.T) {
	f := dbtest.Setup(t)
	srv := newTestServer(f)

	req := httptest.NewRequest("GET", "/v1/numbers", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no key got %d, want 401", rec.Code)
	}
}

func TestAuthRequiredRejectsBadKey(t *testing.T) {
	f := dbtest.Setup(t)
	srv := newTestServer(f)

	req := httptest.NewRequest("GET", "/v1/numbers", nil)
	req.Header.Set("Authorization", "Bearer tk_test_nonexistent")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad key got %d, want 401", rec.Code)
	}
}

// Sanity check: with the fixture's key the listing endpoint responds 200
// and the body is a JSON array (`[]` when empty, not `null`).
func TestListNumbersWithValidKey(t *testing.T) {
	f := dbtest.Setup(t)
	srv := newTestServer(f)

	req := httptest.NewRequest("GET", "/v1/numbers", nil)
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.HasPrefix(body, "[") {
		t.Errorf("body should be JSON array, got %q", body)
	}
}
