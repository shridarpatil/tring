package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/flowstore"
)

// otherTenant inserts a second tenant with one trunk and one flow, the
// resources the fixture tenant must not be able to reference.
func otherTenant(t *testing.T, f *dbtest.Fixture) (tenant, trunk, flow uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ('other', 'other.tring.local', $1, 'deprecated--') RETURNING id
	`, "deprecated-"+uuid.NewString()).Scan(&tenant); err != nil {
		t.Fatalf("insert other tenant: %v", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO trunks (tenant_id, name, provider_type, register, active)
		VALUES ($1, 'other-trunk', 'generic', false, true) RETURNING id
	`, tenant).Scan(&trunk); err != nil {
		t.Fatalf("insert other trunk: %v", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, `
		INSERT INTO flows (tenant_id, name, definition, is_active)
		VALUES ($1, 'other-flow', '{"version":2,"nodes":[],"edges":[],"entry_node":""}'::jsonb, true)
		RETURNING id
	`, tenant).Scan(&flow); err != nil {
		t.Fatalf("insert other flow: %v", err)
	}
	return tenant, trunk, flow
}

func serve(t *testing.T, s *Server, f *dbtest.Fixture, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, method, target, body))
	return rec
}

func TestCreateNumberRejectsForeignTrunkAndFlow(t *testing.T) {
	f := dbtest.Setup(t)
	_, trunk, flow := otherTenant(t, f)
	s := newTestServer(f)

	cases := map[string]string{
		"foreign trunk": `{"e164":"+919800000001","type":"outbound","trunk_id":"` + trunk.String() + `"}`,
		"foreign flow":  `{"e164":"+919800000002","type":"inbound","flow_id":"` + flow.String() + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := serve(t, s, f, "POST", "/v1/numbers", body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPatchNumberRejectsForeignFlow(t *testing.T) {
	f := dbtest.Setup(t)
	_, _, flow := otherTenant(t, f)
	s := newTestServer(f)

	rec := serve(t, s, f, "POST", "/v1/numbers", `{"e164":"+919800000003","type":"inbound"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT id FROM numbers WHERE e164 = '+919800000003'`).Scan(&id); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	rec = serve(t, s, f, "PATCH", "/v1/numbers/"+id, `{"flow_id":"`+flow.String()+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateNumberRejectsInboundDIDHeldByAnotherTenant(t *testing.T) {
	f := dbtest.Setup(t)
	other, _, _ := otherTenant(t, f)
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, active) VALUES ($1, '+919800000004', 'inbound', true)
	`, other); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestServer(f)

	rec := serve(t, s, f, "POST", "/v1/numbers", `{"e164":"+919800000004","type":"inbound"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	// Outbound use of the same digits is not inbound routing and stays allowed.
	var trunk uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO trunks (tenant_id, name, provider_type, register, active)
		VALUES ($1, 'own-trunk', 'generic', false, true) RETURNING id
	`, f.TenantID).Scan(&trunk); err != nil {
		t.Fatalf("insert trunk: %v", err)
	}
	rec = serve(t, s, f, "POST", "/v1/numbers",
		`{"e164":"+919800000004","type":"outbound","trunk_id":"`+trunk.String()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("outbound create: %d %s", rec.Code, rec.Body.String())
	}
}

// Rows that predate the API checks: a number pointing at another tenant's flow
// must not run it, and a duplicated inbound DID routes to the first registrant.
func TestResolveDIDIgnoresForeignFlowAndPrefersFirstRegistrant(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	other, _, otherFlow := otherTenant(t, f)
	store := flowstore.New(f.DB)

	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, flow_id, active) VALUES ($1, '+919800000005', 'inbound', $2, true)
	`, f.TenantID, otherFlow); err != nil {
		t.Fatalf("seed foreign-flow number: %v", err)
	}
	tenant, flowID, err := store.ResolveDID(ctx, "+919800000005")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tenant != f.TenantID || flowID != "" {
		t.Fatalf("resolve = (%s, %q), want (%s, \"\"): a foreign flow must not resolve", tenant, flowID, f.TenantID)
	}

	ownFlow := seedFlow(t, f, "own-flow")
	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, flow_id, active, created_at)
		VALUES ($1, '+919800000006', 'inbound', $2, true, now() - interval '1 day')
	`, f.TenantID, ownFlow); err != nil {
		t.Fatalf("seed first registrant: %v", err)
	}
	if _, err := f.DB.Pool.Exec(ctx, `
		INSERT INTO numbers (tenant_id, e164, type, flow_id, active) VALUES ($1, '+919800000006', 'inbound', $2, true)
	`, other, otherFlow); err != nil {
		t.Fatalf("seed duplicate: %v", err)
	}
	tenant, flowID, err = store.ResolveDID(ctx, "+919800000006")
	if err != nil {
		t.Fatalf("resolve duplicate: %v", err)
	}
	if tenant != f.TenantID || flowID != ownFlow {
		t.Fatalf("resolve = (%s, %q), want first registrant (%s, %q)", tenant, flowID, f.TenantID, ownFlow)
	}
}

// select-outbound-trunks must not pick up another tenant's trunk even if a
// number row points at it.
func TestSelectTrunksIgnoresForeignTrunk(t *testing.T) {
	f := dbtest.Setup(t)
	_, foreign, _ := otherTenant(t, f)
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, trunk_id, priority, active)
		VALUES ($1, '+919800000007', 'outbound', $2, 100, true)
	`, f.TenantID, foreign); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newServerFor(f)
	trunks, _ := s.selectTrunks(context.Background(), &Tenant{ID: f.TenantID, OutboundStrategy: "failover"})
	for _, tr := range trunks {
		if tr.E164 == "+919800000007" {
			t.Fatalf("selected a number routed through another tenant's trunk: %+v", tr)
		}
	}
}
