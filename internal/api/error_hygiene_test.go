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

// A DB failure while picking trunks is a server error, not a caller error
// carrying the pgx error text.
func TestSelectTrunksDBFailureIsInternal(t *testing.T) {
	f := dbtest.Setup(t)
	s := newServerFor(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // every query now fails with a non-user error

	if _, err := s.selectTrunks(ctx, &Tenant{ID: f.TenantID}); !errors.Is(err, errInternal) {
		t.Errorf("selectTrunks on DB failure = %v, want errInternal", err)
	}
}
