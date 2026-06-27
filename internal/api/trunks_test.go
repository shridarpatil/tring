package api

import (
	"context"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/queries"
)

// insertOutbound creates a trunk + an outbound number bound to it. Tests
// stack these and then call selectTrunks to verify ordering / strategy.
// The trunk's provider_type drives normalization (kept as the caller's
// `provider` arg for test brevity — it's both the trunk name and the
// provider_type).
func insertOutbound(t *testing.T, f *dbtest.Fixture, e164, provider string, priority int, active bool) {
	t.Helper()
	ctx := context.Background()
	var trunkID string
	err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO trunks (tenant_id, name, provider_type, register, active)
		VALUES ($1, $2 || '_' || md5(random()::text), $2, false, $3)
		RETURNING id
	`, f.TenantID, provider, active).Scan(&trunkID)
	if err != nil {
		t.Fatalf("insert trunk %s: %v", provider, err)
	}
	_, err = f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, trunk_id, priority, active)
		VALUES ($1, $2, 'outbound', $3, $4, $5)
	`, f.TenantID, e164, trunkID, priority, active)
	if err != nil {
		t.Fatalf("insert number %s: %v", e164, err)
	}
}

// newServerFor wires a minimal Server around the fixture DB. We don't need
// the FS client or notifier — selectTrunks only touches s.db, s.q, and s.rr*.
func newServerFor(f *dbtest.Fixture) *Server {
	return &Server{db: f.DB, q: queries.MustLoad()}
}

func TestSelectTrunksFailoverReturnsAllByPriority(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 100, true)
	insertOutbound(t, f, "+918002222222", "jio", 50, true) // lower priority wins
	insertOutbound(t, f, "+918003333333", "tata", 200, true)

	s := newServerFor(f)
	tenant := &Tenant{ID: f.TenantID, OutboundStrategy: "failover"}

	trunks, err := s.selectTrunks(context.Background(), tenant)
	if err != nil {
		t.Fatalf("selectTrunks: %v", err)
	}
	if len(trunks) != 3 {
		t.Fatalf("got %d trunks, want 3 (failover returns all active)", len(trunks))
	}
	// Gateway is now derived from trunk UUID (t_<short>), not the
	// provider string. Assert order via ProviderType instead.
	if trunks[0].ProviderType != "jio" {
		t.Errorf("first trunk type = %q, want jio (lowest priority)", trunks[0].ProviderType)
	}
	if trunks[2].ProviderType != "tata" {
		t.Errorf("last trunk type = %q, want tata (highest priority)", trunks[2].ProviderType)
	}
}

func TestSelectTrunksManualReturnsLowestPriorityOnly(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 100, true)
	insertOutbound(t, f, "+918002222222", "jio", 50, true) // would win

	s := newServerFor(f)
	tenant := &Tenant{ID: f.TenantID, OutboundStrategy: "manual"}

	trunks, err := s.selectTrunks(context.Background(), tenant)
	if err != nil {
		t.Fatalf("selectTrunks: %v", err)
	}
	if len(trunks) != 1 || trunks[0].ProviderType != "jio" {
		t.Errorf("manual got %+v, want [{jio}]", trunks)
	}
}

func TestSelectTrunksRoundRobinCycles(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 100, true)
	insertOutbound(t, f, "+918002222222", "jio", 100, true)
	insertOutbound(t, f, "+918003333333", "tata", 100, true)

	s := newServerFor(f)
	tenant := &Tenant{ID: f.TenantID, OutboundStrategy: "round_robin"}

	// First three calls should each pick a different trunk; fourth should
	// wrap back around. We don't pin the starting provider — just that the
	// sequence cycles through three distinct providers and then repeats.
	seen := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		trunks, err := s.selectTrunks(context.Background(), tenant)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(trunks) != 1 {
			t.Fatalf("round_robin returned %d trunks, want 1", len(trunks))
		}
		seen = append(seen, trunks[0].ProviderType)
	}
	if seen[0] == seen[1] || seen[1] == seen[2] || seen[0] == seen[2] {
		t.Errorf("round_robin not cycling, got %v", seen)
	}
	if seen[0] != seen[3] {
		t.Errorf("round_robin should wrap on 4th call; seen=%v", seen)
	}
}

// load_based is documented as reserved in trunks.go and the schema accepts
// the value. The runtime MUST 501 — otherwise an operator who set this
// strategy would get silent fallback to whatever Go map iteration order
// FS happens to choose, which is worse than a clear error.
func TestSelectTrunksLoadBasedReturnsError(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 100, true)

	s := newServerFor(f)
	tenant := &Tenant{ID: f.TenantID, OutboundStrategy: "load_based"}

	if _, err := s.selectTrunks(context.Background(), tenant); err == nil {
		t.Error("load_based should return error, got nil")
	}
}

func TestSelectTrunksEmptyStrategyDefaultsToFailover(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 50, true)
	insertOutbound(t, f, "+918002222222", "jio", 100, true)

	s := newServerFor(f)
	tenant := &Tenant{ID: f.TenantID} // OutboundStrategy: ""

	trunks, err := s.selectTrunks(context.Background(), tenant)
	if err != nil {
		t.Fatalf("selectTrunks: %v", err)
	}
	if len(trunks) != 2 {
		t.Errorf("empty strategy got %d trunks, want 2 (failover default)", len(trunks))
	}
}

// buildBridgeLegs is the dial-string construction for the two-PSTN-number
// connect/callback flow. These tests pin the exact strings FreeSWITCH
// expects so the curl works the moment a real trunk is live — no DB needed.

func TestBuildBridgeLegsWithTrunk(t *testing.T) {
	trunks := []trunk{{E164: "+918001234567", Gateway: "tata", ProviderType: "tata"}}
	aLeg, bLegs, callerID := buildBridgeLegs(trunks, "+919876543210", "+919812345678")

	if aLeg != "sofia/gateway/tata/+919876543210" {
		t.Errorf("aLeg = %q, want sofia/gateway/tata/+919876543210", aLeg)
	}
	if callerID != "+918001234567" {
		t.Errorf("callerID = %q, want the trunk DID +918001234567", callerID)
	}
	if len(bLegs) != 1 {
		t.Fatalf("want 1 b-leg, got %d", len(bLegs))
	}
	want := "[effective_caller_id_number=+918001234567]sofia/gateway/tata/+919812345678"
	if bLegs[0] != want {
		t.Errorf("bLeg = %q, want %q", bLegs[0], want)
	}
}

func TestBuildBridgeLegsLoopbackFallback(t *testing.T) {
	// No trunk (dev) → loopback dial strings, no caller ID.
	aLeg, bLegs, callerID := buildBridgeLegs(nil, "1001", "1002")
	if aLeg != "loopback/1001" {
		t.Errorf("aLeg = %q, want loopback/1001", aLeg)
	}
	if len(bLegs) != 1 || bLegs[0] != "loopback/1002" {
		t.Errorf("bLegs = %v, want [loopback/1002]", bLegs)
	}
	if callerID != "" {
		t.Errorf("callerID = %q, want empty (no DID in loopback)", callerID)
	}
}

func TestBuildBridgeLegsUsesPrimaryTrunk(t *testing.T) {
	// With multiple trunks, the bridge uses the first (primary) for both legs.
	trunks := []trunk{
		{E164: "+918002222222", Gateway: "jio", ProviderType: "jio"},
		{E164: "+918003333333", Gateway: "airtel", ProviderType: "airtel"},
	}
	aLeg, _, callerID := buildBridgeLegs(trunks, "+919876543210", "+919812345678")
	if aLeg != "sofia/gateway/jio/+919876543210" {
		t.Errorf("aLeg = %q, want primary (jio) gateway", aLeg)
	}
	if callerID != "+918002222222" {
		t.Errorf("callerID = %q, want primary DID", callerID)
	}
}

func TestSelectTrunksInactiveExcluded(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 100, true)
	insertOutbound(t, f, "+918002222222", "jio", 50, false) // would win on priority, but inactive

	s := newServerFor(f)
	tenant := &Tenant{ID: f.TenantID, OutboundStrategy: "failover"}

	trunks, err := s.selectTrunks(context.Background(), tenant)
	if err != nil {
		t.Fatalf("selectTrunks: %v", err)
	}
	if len(trunks) != 1 || trunks[0].ProviderType != "airtel" {
		t.Errorf("got %+v, want only airtel (jio is inactive)", trunks)
	}
}
