package api

import (
	"context"
	"testing"

	"github.com/whatomate/tring/internal/dbtest"
)

// insertOutbound adds an outbound number for the fixture tenant. Tests
// stack these and then call selectTrunks to verify ordering / strategy.
func insertOutbound(t *testing.T, f *dbtest.Fixture, e164, provider string, priority int, active bool) {
	t.Helper()
	_, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, priority, active)
		VALUES ($1, $2, 'outbound', $3, $4, $5)
	`, f.TenantID, e164, provider, priority, active)
	if err != nil {
		t.Fatalf("insert number %s: %v", e164, err)
	}
}

// newServerFor wires a minimal Server around the fixture DB. We don't need
// the FS client or notifier — selectTrunks only touches s.db and s.rr*.
func newServerFor(f *dbtest.Fixture) *Server {
	return &Server{db: f.DB}
}

func TestSelectTrunksFailoverReturnsAllByPriority(t *testing.T) {
	f := dbtest.Setup(t)
	insertOutbound(t, f, "+918001111111", "airtel", 100, true)
	insertOutbound(t, f, "+918002222222", "jio", 50, true)   // lower priority wins
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
	if trunks[0].Provider != "jio" {
		t.Errorf("first trunk = %q, want jio (lowest priority)", trunks[0].Provider)
	}
	if trunks[2].Provider != "tata" {
		t.Errorf("last trunk = %q, want tata (highest priority)", trunks[2].Provider)
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
	if len(trunks) != 1 || trunks[0].Provider != "jio" {
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
		seen = append(seen, trunks[0].Provider)
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
	if len(trunks) != 1 || trunks[0].Provider != "airtel" {
		t.Errorf("got %+v, want only airtel (jio is inactive)", trunks)
	}
}
