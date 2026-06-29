package api

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
)

// A Server with a hub but no DB still streams: the snapshot is best-effort, so
// the handler emits an (empty) snapshot frame then forwards hub events.
func TestDashboardStreamSnapshotThenEvent(t *testing.T) {
	hub := events.NewTestHub() // test-only constructor (no Redis), added in this task
	hub.Forward(events.TypeCallEnded)
	srv := &Server{hub: hub, log: slog.Default()}

	tenantID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/v1/stream/dashboard", nil)
	r = r.WithContext(withTenantForTest(ctx, tenantID)) // helper below
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		srv.handleDashboardStream(w, r)
		close(done)
	}()

	// Give the handler a moment to write the snapshot and subscribe, then push.
	time.Sleep(50 * time.Millisecond)
	hub.DispatchForTest(tenantID.String(), events.Frame(events.TypeCallEnded, []byte(`{"call_id":"c1"}`)))
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: snapshot") {
		t.Fatalf("missing snapshot frame:\n%s", body)
	}
	if !strings.Contains(body, "event: call.ended") {
		t.Fatalf("missing forwarded event:\n%s", body)
	}
}

// withTenantForTest injects a Tenant into the request context using the same key
// tenantFromCtx reads.
func withTenantForTest(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantCtxKey, &Tenant{ID: id})
}
