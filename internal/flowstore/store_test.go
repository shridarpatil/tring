package flowstore

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

const minimalFlow = `{"version":2,"entry_node":"start",
  "nodes":[{"id":"start","type":"start","config":{}},{"id":"end","type":"hangup","config":{}}],
  "edges":[{"from":"start","to":"end","condition":"default"}]}`

func insertFlow(t *testing.T, f *dbtest.Fixture, tenant uuid.UUID, name string) string {
	t.Helper()
	var id uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO flows (tenant_id, name, definition, is_active)
		VALUES ($1, $2, $3::jsonb, true)
		RETURNING id
	`, tenant, name, minimalFlow).Scan(&id); err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	return id.String()
}

// A flow id only resolves inside its own tenant: an agent who sends another
// tenant's flow_id in a join must not run (or name) that tenant's IVR.
func TestLoadIsTenantScoped(t *testing.T) {
	f := dbtest.Setup(t) // skips unless TRING_TEST_DSN set
	ctx := context.Background()
	s := New(f.DB)

	var other uuid.UUID
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ('other', 'other.tring.local', $1, 'deprecated--') RETURNING id
	`, "deprecated-"+uuid.NewString()).Scan(&other); err != nil {
		t.Fatalf("insert other tenant: %v", err)
	}
	own := insertFlow(t, f, f.TenantID, "own")
	foreign := insertFlow(t, f, other, "foreign")
	me := f.TenantID.String()

	if _, err := s.Load(ctx, me, own); err != nil {
		t.Errorf("Load own flow: %v", err)
	}
	if n, err := s.FlowName(ctx, me, own); err != nil || n != "own" {
		t.Errorf("FlowName own flow = %q, %v", n, err)
	}
	if _, err := s.Load(ctx, me, foreign); err == nil {
		t.Error("Load of another tenant's flow succeeded")
	}
	if _, err := s.FlowName(ctx, me, foreign); err == nil {
		t.Error("FlowName of another tenant's flow succeeded")
	}
	if _, err := s.Load(ctx, "not-a-uuid", own); err == nil {
		t.Error("Load with a bad tenant id succeeded")
	}
}

// A deactivated inbound number stops routing calls.
func TestResolveDIDIgnoresInactiveNumbers(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	s := New(f.DB)

	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, priority, active)
		VALUES ($1, '+910000000001', 'inbound', 100, true), ($1, '+910000000002', 'inbound', 100, false)
	`, f.TenantID); err != nil {
		t.Fatalf("insert numbers: %v", err)
	}
	if _, _, err := s.ResolveDID(ctx, "+910000000001"); err != nil {
		t.Errorf("active DID: %v", err)
	}
	if _, _, err := s.ResolveDID(ctx, "+910000000002"); err == nil {
		t.Error("inactive DID still resolves")
	}
}
