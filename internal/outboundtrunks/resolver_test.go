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

// insertOutbound seeds a trunk plus an outbound number bound to it and returns
// the number's expected gateway name.
func insertOutbound(t *testing.T, fx *dbtest.Fixture, tenant uuid.UUID, e164 string, priority int, active bool) string {
	t.Helper()
	ctx := context.Background()
	var trunkID uuid.UUID
	if err := fx.DB.Pool.QueryRow(ctx, `
		INSERT INTO trunks (tenant_id, name, provider_type, active)
		VALUES ($1, $2, 'generic', $3)
		RETURNING id
	`, tenant, "trunk-"+e164, active).Scan(&trunkID); err != nil {
		t.Fatalf("insert trunk: %v", err)
	}
	if _, err := fx.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, trunk_id, priority, active)
		VALUES ($1, $2, 'outbound', $3, $4, $5)
	`, fx.TenantID, e164, trunkID, priority, active); err != nil {
		t.Fatalf("insert number: %v", err)
	}
	return freeswitch.GatewayName(trunkID)
}

// TestOutboundTrunks_InactiveExcluded: an inactive number + trunk is skipped
// even when its priority would win.
func TestOutboundTrunks_InactiveExcluded(t *testing.T) {
	fx := dbtest.Setup(t)
	insertOutbound(t, fx, fx.TenantID, "+918001111111", 100, true)
	insertOutbound(t, fx, fx.TenantID, "+918002222222", 50, false)

	trunks, err := New(fx.DB).OutboundTrunks(context.Background(), fx.TenantID)
	if err != nil {
		t.Fatalf("OutboundTrunks: %v", err)
	}
	if len(trunks) != 1 || trunks[0].E164 != "+918001111111" {
		t.Errorf("got %+v, want only +918001111111 (the other is inactive)", trunks)
	}
}

// TestOutboundTrunks_IgnoresForeignTrunk: a number of this tenant pointing at
// another tenant's trunk must never be dialed through.
func TestOutboundTrunks_IgnoresForeignTrunk(t *testing.T) {
	fx := dbtest.Setup(t)
	var other uuid.UUID
	if err := fx.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ('other', 'other.tring.local', $1, 'deprecated--') RETURNING id
	`, "deprecated-"+uuid.NewString()).Scan(&other); err != nil {
		t.Fatalf("insert other tenant: %v", err)
	}
	insertOutbound(t, fx, other, "+919800000007", 100, true)

	trunks, err := New(fx.DB).OutboundTrunks(context.Background(), fx.TenantID)
	if err != nil {
		t.Fatalf("OutboundTrunks: %v", err)
	}
	if len(trunks) != 0 {
		t.Fatalf("selected a number routed through another tenant's trunk: %+v", trunks)
	}
}
