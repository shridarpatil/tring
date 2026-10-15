package outboundtrunks

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/freeswitch"
)

// TestOutboundTrunks_SingleActiveTrunk seeds a tenant with one active
// outbound number joined to one active trunk and asserts OutboundTrunks
// returns it verbatim (default failover strategy, single trunk → the
// selection is a no-op but exercises the full query + join + GatewayName
// path end to end).
func TestOutboundTrunks_SingleActiveTrunk(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()

	var trunkID uuid.UUID
	err := fx.DB.Pool.QueryRow(ctx, `
		INSERT INTO trunks (tenant_id, name, provider_type, active)
		VALUES ($1, $2, $3, true)
		RETURNING id
	`, fx.TenantID, "test-trunk", "loopback").Scan(&trunkID)
	if err != nil {
		t.Fatalf("insert trunk: %v", err)
	}

	const e164 = "+911234567890"
	_, err = fx.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, trunk_id, priority, active)
		VALUES ($1, $2, 'outbound', $3, 100, true)
	`, fx.TenantID, e164, trunkID)
	if err != nil {
		t.Fatalf("insert number: %v", err)
	}

	r := New(fx.DB)
	trunks, err := r.OutboundTrunks(ctx, fx.TenantID)
	if err != nil {
		t.Fatalf("OutboundTrunks: %v", err)
	}
	if len(trunks) != 1 {
		t.Fatalf("want 1 trunk, got %d (%+v)", len(trunks), trunks)
	}
	if trunks[0].E164 != e164 {
		t.Errorf("E164 = %q, want %q", trunks[0].E164, e164)
	}
	wantName := freeswitch.GatewayName(trunkID)
	if trunks[0].Name != wantName {
		t.Errorf("Name = %q, want %q", trunks[0].Name, wantName)
	}
}

// TestOutboundTrunks_NoTrunks asserts an empty (nil,nil) result for a tenant
// with no active outbound numbers — the signaling handler's "no trunk" path.
func TestOutboundTrunks_NoTrunks(t *testing.T) {
	fx := dbtest.Setup(t)
	r := New(fx.DB)
	trunks, err := r.OutboundTrunks(context.Background(), fx.TenantID)
	if err != nil {
		t.Fatalf("OutboundTrunks: %v", err)
	}
	if len(trunks) != 0 {
		t.Fatalf("want 0 trunks, got %d (%+v)", len(trunks), trunks)
	}
}

// TestOwnsNumber: any active number of the tenant — inbound included (a mask
// number agents call back) — may be presented; others' and inactive ones not.
func TestOwnsNumber(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, active) VALUES
		  ($1, '02200000001', 'inbound', true),
		  ($1, '02200000002', 'inbound', false)
	`, fx.TenantID); err != nil {
		t.Fatalf("insert numbers: %v", err)
	}
	r := New(fx.DB)
	for num, want := range map[string]bool{"02200000001": true, "02200000002": false, "02299999999": false} {
		got, err := r.OwnsNumber(ctx, fx.TenantID, num)
		if err != nil || got != want {
			t.Errorf("OwnsNumber(%s) = %v, %v; want %v", num, got, err, want)
		}
	}
	if got, _ := r.OwnsNumber(ctx, uuid.New(), "02200000001"); got {
		t.Error("another tenant must not own this tenant's number")
	}
}
