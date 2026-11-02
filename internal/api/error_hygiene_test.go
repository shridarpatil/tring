package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
