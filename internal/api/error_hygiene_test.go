package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// A 5xx must answer with the generic message only; the detail goes to the
// server log, never the client.
func TestWriteServerErrorHidesDetail(t *testing.T) {
	var logBuf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&logBuf, nil))}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/v1/numbers/x", nil)

	detail := errors.New(`ERROR: column "secret_col" does not exist (SQLSTATE 42703)`)
	s.writeServerError(w, r, http.StatusInternalServerError, "update failed", detail)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "secret_col") || !strings.Contains(body, `"update failed"`) {
		t.Errorf("body = %s, want only the generic message", body)
	}
	if !strings.Contains(logBuf.String(), "secret_col") {
		t.Errorf("detail not logged: %s", logBuf.String())
	}
}

// Originate errors: 4xx stay descriptive (they are about the caller's input),
// 5xx/502 are generic (they carry pgx errors / FreeSWITCH replies).
func TestWriteCallError(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	cases := []struct {
		code     int
		err      error
		wantBody string
		leak     string
	}{
		{http.StatusBadRequest, errors.New(`caller_id "123" is not one of this tenant's numbers`), "is not one of this tenant's numbers", ""},
		{http.StatusNotFound, errors.New("agent not found"), "agent not found", ""},
		{http.StatusBadGateway, errors.New("originate failed: -ERR NORMAL_TEMPORARY_FAILURE sofia/gateway/t_1234"), "originate failed", "sofia/gateway"},
		{http.StatusInternalServerError, errors.New("insert call: ERROR: relation \"calls\" (SQLSTATE 42P01)"), "internal error", "SQLSTATE"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.writeCallError(w, httptest.NewRequest(http.MethodPost, "/v1/calls", nil), c.code, c.err)
		if w.Code != c.code {
			t.Errorf("code = %d, want %d", w.Code, c.code)
		}
		body := w.Body.String()
		if !strings.Contains(body, c.wantBody) {
			t.Errorf("%d: body %s, want %q", c.code, body, c.wantBody)
		}
		if c.leak != "" && strings.Contains(body, c.leak) {
			t.Errorf("%d: body %s leaks %q", c.code, body, c.leak)
		}
	}
}

// A DB failure while validating caller_id or picking trunks is a server
// error (500), not a 400 carrying the pgx error text.
func TestOriginateDBFailureIsInternal(t *testing.T) {
	f := dbtest.Setup(t)
	s := newServerFor(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // every query now fails with a non-user error

	if err := s.checkCallerID(ctx, f.TenantID, "02200000123"); !errors.Is(err, errInternal) {
		t.Errorf("checkCallerID on DB failure = %v, want errInternal", err)
	}
	if _, err := s.selectTrunks(ctx, &Tenant{ID: f.TenantID}); !errors.Is(err, errInternal) {
		t.Errorf("selectTrunks on DB failure = %v, want errInternal", err)
	}
	_, code, _ := s.originateForAgent(ctx, &Tenant{ID: f.TenantID}, uuid.New(), "+919800000000", "", "02200000123")
	if code != http.StatusInternalServerError {
		t.Errorf("originateForAgent code = %d, want 500", code)
	}
	_, code, _ = s.originateBridge(ctx, &Tenant{ID: f.TenantID}, "+919800000000", "+919800000001")
	if code != http.StatusInternalServerError {
		t.Errorf("originateBridge code = %d, want 500", code)
	}
}
