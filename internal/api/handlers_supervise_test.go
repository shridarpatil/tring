package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// insertSuperviseCall inserts a minimal 'calls' row with the given status
// and room_id so handleSupervise has a live web call to resolve. Fills
// every NOT NULL column per 0001_initial_schema.up.sql / 0019 (source) /
// 0020 (room_id).
func insertSuperviseCall(t *testing.T, f *dbtest.Fixture, status, roomID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, source, room_id)
		VALUES ($1, 'inbound', $2, 'visitor', 'Support', $3, 'web', $4)
		RETURNING id
	`, f.TenantID, status, uuid.NewString(), roomID).Scan(&id)
	if err != nil {
		t.Fatalf("insert call: %v", err)
	}
	return id
}

func TestSuperviseMintsToken(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE users SET can_supervise=true WHERE id=$1", f.UserID); err != nil {
		t.Fatalf("grant can_supervise: %v", err)
	}
	callID := insertSuperviseCall(t, f, "answered", f.TenantID.String()+"/room-42")
	s := newTestServer(f)

	req := authedRequest(t, s, f, "POST", "/v1/calls/"+callID.String()+"/supervise", "")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp superviseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.Room != "room-42" {
		t.Errorf("room = %q, want room-42", resp.Room)
	}
	if resp.Token == "" {
		t.Errorf("token is empty")
	}
	if resp.GatewayURL != "http://gw.test" {
		t.Errorf("gateway url = %q, want http://gw.test", resp.GatewayURL)
	}
}

// TestSuperviseDeniedWithoutPermission relies on the fixture user's default
// can_supervise being false — see dbtest.Setup, which inserts the console
// user via inline SQL that omits can_supervise, so it takes the migration
// column default (false). Explicitly set it anyway so the test doesn't
// silently depend on that default staying put.
func TestSuperviseDeniedWithoutPermission(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE users SET can_supervise=false WHERE id=$1", f.UserID); err != nil {
		t.Fatalf("revoke can_supervise: %v", err)
	}
	callID := insertSuperviseCall(t, f, "answered", f.TenantID.String()+"/room-9")
	s := newTestServer(f)

	req := authedRequest(t, s, f, "POST", "/v1/calls/"+callID.String()+"/supervise", "")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSuperviseRejectsAPIKey(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	req := httptest.NewRequest(http.MethodPost, "/v1/calls/"+uuid.NewString()+"/supervise", nil)
	req.Header.Set("Authorization", "Bearer tk_fake")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}
