package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/whatomate/tring/internal/dbtest"
)

// resolveFlow is the only piece of HandleInboundCall that can be tested
// without a real ESL connection — everything past that point needs a
// channel to drive. These cover the four user-visible outcomes:
//
//   - DID isn't registered                 → pgx.ErrNoRows
//   - DID registered, no flow attached     → (Flow == nil)
//   - DID registered, active flow attached → (Flow != nil)
//   - DID registered, INACTIVE flow        → (Flow == nil) — orchestrator treats it as "no flow"

func seedInbound(t *testing.T, f *dbtest.Fixture, e164 string, flowID *string) {
	t.Helper()
	if flowID == nil {
		_, err := f.DB.Pool.Exec(context.Background(), `
			INSERT INTO numbers (tenant_id, e164, type, provider, priority, active)
			VALUES ($1, $2, 'inbound', 'generic', 100, true)
		`, f.TenantID, e164)
		if err != nil {
			t.Fatalf("insert number: %v", err)
		}
		return
	}
	_, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, priority, active, flow_id)
		VALUES ($1, $2, 'inbound', 'generic', 100, true, $3)
	`, f.TenantID, e164, *flowID)
	if err != nil {
		t.Fatalf("insert number: %v", err)
	}
}

func seedFlow(t *testing.T, f *dbtest.Fixture, definition string, active bool) string {
	t.Helper()
	var id string
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO flows (tenant_id, name, definition, is_active)
		VALUES ($1, $2, $3::jsonb, $4)
		RETURNING id
	`, f.TenantID, "test-flow", definition, active).Scan(&id)
	if err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	return id
}

func TestResolveFlowReturnsErrorForUnknownNumber(t *testing.T) {
	f := dbtest.Setup(t)
	exec := NewExecutor(f.DB)

	_, err := exec.resolveFlow(context.Background(), "+919999999999")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("err = %v, want pgx.ErrNoRows", err)
	}
}

func TestResolveFlowReturnsNilFlowWhenDIDHasNone(t *testing.T) {
	f := dbtest.Setup(t)
	seedInbound(t, f, "+918001112222", nil)
	exec := NewExecutor(f.DB)

	got, err := exec.resolveFlow(context.Background(), "+918001112222")
	if err != nil {
		t.Fatalf("resolveFlow: %v", err)
	}
	if got.Flow != nil {
		t.Errorf("Flow = %+v, want nil (DID has no flow attached)", got.Flow)
	}
	if got.TenantID != f.TenantID {
		t.Errorf("TenantID = %s, want %s", got.TenantID, f.TenantID)
	}
}

func TestResolveFlowReturnsParsedFlow(t *testing.T) {
	f := dbtest.Setup(t)
	flowID := seedFlow(t, f,
		`{"version":2,"entry_node":"n1","nodes":[{"id":"n1","type":"hangup","config":{}}],"edges":[]}`,
		true,
	)
	seedInbound(t, f, "+918002223333", &flowID)
	exec := NewExecutor(f.DB)

	got, err := exec.resolveFlow(context.Background(), "+918002223333")
	if err != nil {
		t.Fatalf("resolveFlow: %v", err)
	}
	if got.Flow == nil {
		t.Fatal("Flow = nil, want parsed flow")
	}
	if got.Flow.EntryNode != "n1" {
		t.Errorf("EntryNode = %q, want n1", got.Flow.EntryNode)
	}
	if len(got.Flow.Nodes) != 1 {
		t.Errorf("Nodes len = %d, want 1", len(got.Flow.Nodes))
	}
}

func TestResolveFlowSkipsInactiveFlow(t *testing.T) {
	f := dbtest.Setup(t)
	flowID := seedFlow(t, f,
		`{"version":2,"entry_node":"n1","nodes":[{"id":"n1","type":"hangup","config":{}}],"edges":[]}`,
		false, // is_active=false → executor must treat as "no flow"
	)
	seedInbound(t, f, "+918003334444", &flowID)
	exec := NewExecutor(f.DB)

	got, err := exec.resolveFlow(context.Background(), "+918003334444")
	if err != nil {
		t.Fatalf("resolveFlow: %v", err)
	}
	if got.Flow != nil {
		t.Errorf("Flow = %+v, want nil (flow is inactive)", got.Flow)
	}
}

func TestResolveFlowErrorsOnEmptyEntryNode(t *testing.T) {
	f := dbtest.Setup(t)
	flowID := seedFlow(t, f,
		`{"version":2,"entry_node":"","nodes":[],"edges":[]}`,
		true,
	)
	seedInbound(t, f, "+918004445555", &flowID)
	exec := NewExecutor(f.DB)

	_, err := exec.resolveFlow(context.Background(), "+918004445555")
	if err == nil || !strings.Contains(err.Error(), "entry node") {
		t.Errorf("err = %v, want error containing 'entry node'", err)
	}
}
