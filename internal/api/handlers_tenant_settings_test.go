package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// A PATCH of transfer_failed_message round-trips through get-tenant-settings.
func TestTenantTransferMessageRoundTrip(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	ctx := context.Background()
	s := newTestServer(f)

	msg := "Nobody home, please retry."
	_, err := f.DB.Pool.Exec(ctx, s.q.UpdateTenant, nil, nil, f.TenantID, nil, nil, nil, &msg, nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var got, hm string
	var rec bool
	var mx, rate int
	if err := f.DB.Pool.QueryRow(ctx, s.q.GetTenantSettings, f.TenantID).Scan(&hm, &rec, &mx, &got, &rate); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != msg {
		t.Fatalf("message = %q, want %q", got, msg)
	}
}

// recording_sample_rate defaults to 8 kHz, accepts only Opus rates, and the
// value saved is what the recorder reads at call start (get-tenant-record-calls).
func TestTenantRecordingSampleRate(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	s := newTestServer(f)

	get := func() Tenant {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/tenants/me", nil).WithContext(withTenantForTest(ctx, f.TenantID))
		s.handleGetMyTenant(w, r)
		var out Tenant
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}
	patch := func(body string) int {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/v1/tenants/me", strings.NewReader(body)).WithContext(withTenantForTest(ctx, f.TenantID))
		s.handlePatchMyTenant(w, r)
		return w.Code
	}

	if got := get().RecordingSampleRate; got != 8000 {
		t.Fatalf("default rate = %d, want 8000", got)
	}
	for _, bad := range []string{`{"recording_sample_rate": 44100}`, `{"recording_sample_rate": 0}`} {
		if code := patch(bad); code != http.StatusBadRequest {
			t.Fatalf("PATCH %s = %d, want 400", bad, code)
		}
	}
	if code := patch(`{"recording_sample_rate": 16000}`); code != http.StatusOK {
		t.Fatalf("PATCH 16000 = %d, want 200", code)
	}
	if got := get().RecordingSampleRate; got != 16000 {
		t.Fatalf("rate after PATCH = %d, want 16000", got)
	}
	// Another setting's PATCH leaves the rate alone.
	if code := patch(`{"record_calls": true}`); code != http.StatusOK {
		t.Fatalf("PATCH record_calls = %d", code)
	}
	var on bool
	var rate int
	if err := f.DB.Pool.QueryRow(ctx, s.q.GetTenantRecordCalls, f.TenantID).Scan(&on, &rate); err != nil {
		t.Fatalf("record policy: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("recorder reads rate %d, want 16000", rate)
	}
}
