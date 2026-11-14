package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/flowstore"
)

// A PATCH of transfer_failed_message round-trips through get-tenant-settings.
func TestTenantTransferMessageRoundTrip(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	ctx := context.Background()
	s := newTestServer(f)

	msg := "Nobody home, please retry."
	_, err := f.DB.Pool.Exec(ctx, s.q.UpdateTenant, nil, nil, f.TenantID, nil, nil, nil, &msg, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var got, hm, format string
	var rec bool
	var mx, rate int
	if err := f.DB.Pool.QueryRow(ctx, s.q.GetTenantSettings, f.TenantID).Scan(&hm, &rec, &mx, &got, &rate, &format, new(bool), new(bool), new(bool), new(int)); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != msg {
		t.Fatalf("message = %q, want %q", got, msg)
	}
}

// recording_sample_rate / recording_format default to 8 kHz Ogg, accept only
// the supported values, and what's saved is what the recorder reads at call
// start (get-tenant-record-calls).
func TestTenantRecordingSettings(t *testing.T) {
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

	if got := get(); got.RecordingSampleRate != 8000 || got.RecordingFormat != "ogg" {
		t.Fatalf("defaults = %d %q, want 8000 ogg", got.RecordingSampleRate, got.RecordingFormat)
	}
	for _, bad := range []string{`{"recording_sample_rate": 44100}`, `{"recording_sample_rate": 0}`, `{"recording_format": "flac"}`, `{"recording_format": ""}`} {
		if code := patch(bad); code != http.StatusBadRequest {
			t.Fatalf("PATCH %s = %d, want 400", bad, code)
		}
	}
	if code := patch(`{"recording_sample_rate": 16000, "recording_format": "mp3"}`); code != http.StatusOK {
		t.Fatalf("PATCH 16000 mp3 = %d, want 200", code)
	}
	if got := get(); got.RecordingSampleRate != 16000 || got.RecordingFormat != "mp3" {
		t.Fatalf("after PATCH = %d %q, want 16000 mp3", got.RecordingSampleRate, got.RecordingFormat)
	}
	// Another setting's PATCH leaves the rate alone.
	if code := patch(`{"record_calls": true}`); code != http.StatusOK {
		t.Fatalf("PATCH record_calls = %d", code)
	}
	var on bool
	var rate int
	var format string
	if err := f.DB.Pool.QueryRow(ctx, s.q.GetTenantRecordCalls, f.TenantID).Scan(&on, &rate, &format); err != nil {
		t.Fatalf("record policy: %v", err)
	}
	if rate != 16000 || format != "mp3" {
		t.Fatalf("recorder reads %d %q, want 16000 mp3", rate, format)
	}
}

// transfer_timeout_seconds defaults to 60, accepts 5–600, and what's saved is
// what the gateway reads when an agent transfers (get-tenant-transfer-timeout).
func TestTenantTransferTimeout(t *testing.T) {
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

	if got := get().TransferTimeoutSeconds; got != 60 {
		t.Fatalf("default transfer_timeout_seconds = %d, want 60", got)
	}
	for _, bad := range []string{`{"transfer_timeout_seconds":4}`, `{"transfer_timeout_seconds":601}`} {
		if code := patch(bad); code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", bad, code)
		}
	}
	if code := patch(`{"transfer_timeout_seconds":45}`); code != http.StatusOK {
		t.Fatalf("PATCH 45 = %d, want 200", code)
	}
	if got := get().TransferTimeoutSeconds; got != 45 {
		t.Fatalf("transfer_timeout_seconds = %d, want 45", got)
	}
	d, err := flowstore.New(f.DB).TenantTransferTimeout(ctx, f.TenantID)
	if err != nil || d != 45*time.Second {
		t.Fatalf("gateway read %v (%v), want 45s", d, err)
	}
}
