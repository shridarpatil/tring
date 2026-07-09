package api

import (
	"context"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// A PATCH of transfer_failed_message round-trips through get-tenant-settings.
func TestTenantTransferMessageRoundTrip(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	ctx := context.Background()
	s := newTestServer(f)

	msg := "Nobody home, please retry."
	_, err := f.DB.Pool.Exec(ctx, s.q.UpdateTenant, nil, nil, f.TenantID, nil, nil, nil, &msg, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var got, hm string
	var rec bool
	var mx int
	if err := f.DB.Pool.QueryRow(ctx, s.q.GetTenantSettings, f.TenantID).Scan(&hm, &rec, &mx, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != msg {
		t.Fatalf("message = %q, want %q", got, msg)
	}
}
