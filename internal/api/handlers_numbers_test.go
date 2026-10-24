package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// numberRow returns the (id, active) of the inserted number so the test
// can target it in PATCH calls.
func insertOutboundForHandlerTest(t *testing.T, f *dbtest.Fixture, e164 string, active bool) string {
	t.Helper()
	var id string
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, priority, active)
		VALUES ($1, $2, 'outbound', 'tata', 100, $3)
		RETURNING id
	`, f.TenantID, e164, active).Scan(&id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

// newTestServer builds an *api.Server wired to the fixture DB. The FS
// client and notifier fields stay nil — the handlers under test in this
// file don't call them. Returning the *Server (rather than just the
// router) lets tests mint console JWTs via s.IssueConsoleToken.
func newTestServer(f *dbtest.Fixture) *Server {
	return NewServer(Config{
		DB:               f.DB,
		JWTSecret:        "test-secret",
		GatewayPublicURL: "http://gw.test",
	})
}

// authedRequest builds a PATCH/GET/etc. request pre-stamped with a
// fresh console JWT for the fixture user. Centralises the token-mint
// boilerplate so individual tests focus on the behaviour under test.
func authedRequest(t *testing.T, s *Server, f *dbtest.Fixture, method, target, body string) *http.Request {
	t.Helper()
	tok, _, err := s.IssueConsoleToken(f.UserID, f.TenantID, f.UserEmail, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

// TestPatchNumberLastActiveGuardrail is the headline test for the
// invariant we verified live during the Tring build:
// deactivating the only active outbound trunk must return 409.
func TestPatchNumberLastActiveGuardrail(t *testing.T) {
	f := dbtest.Setup(t)
	id := insertOutboundForHandlerTest(t, f, "+918001234567", true)
	s := newTestServer(f)

	req := authedRequest(t, s, f, "PATCH", "/v1/numbers/"+id, `{"active": false}`)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 — deactivating last active trunk must be rejected", rec.Code)
		t.Logf("body: %s", rec.Body.String())
	}
}

// With a second active trunk in place, deactivating one of them succeeds.
func TestPatchNumberDeactivateWithSpareSucceeds(t *testing.T) {
	f := dbtest.Setup(t)
	primary := insertOutboundForHandlerTest(t, f, "+918001234567", true)
	insertOutboundForHandlerTest(t, f, "+918007654321", true) // the spare
	s := newTestServer(f)

	req := authedRequest(t, s, f, "PATCH", "/v1/numbers/"+primary, `{"active": false}`)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["active"] != false {
		t.Errorf("returned row should be active=false, got %v", out["active"])
	}
}

// ─── flow_id wiring on numbers ──────────────────────────────────────
//
// The IVR executor reads numbers.flow_id to find the flow to run on
// inbound calls. These tests cover the three movements the operator
// makes through the console: register a number with a flow attached,
// assign a flow later via PATCH, and unassign via clear_flow.

// seedFlow inserts a tenant-scoped flow row and returns its UUID.
func seedFlow(t *testing.T, f *dbtest.Fixture, name string) string {
	t.Helper()
	var id string
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO flows (tenant_id, name, definition, is_active)
		VALUES ($1, $2, '{"version":2,"nodes":[],"edges":[],"entry_node":""}'::jsonb, true)
		RETURNING id
	`, f.TenantID, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	return id
}

func TestCreateNumberWithFlow(t *testing.T) {
	f := dbtest.Setup(t)
	flowID := seedFlow(t, f, "greeting-flow")
	s := newTestServer(f)

	body := `{"e164":"+918001112222","type":"inbound","flow_id":"` + flowID + `"}`
	req := authedRequest(t, s, f, "POST", "/v1/numbers", body)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if got := out["flow_id"]; got != flowID {
		t.Errorf("response flow_id = %v, want %s", got, flowID)
	}
}

func TestPatchNumberAssignsFlow(t *testing.T) {
	f := dbtest.Setup(t)
	flowID := seedFlow(t, f, "assign-flow")
	numID := insertInboundForHandlerTest(t, f, "+918002223333")
	s := newTestServer(f)

	req := authedRequest(t, s, f, "PATCH", "/v1/numbers/"+numID, `{"flow_id":"`+flowID+`"}`)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if got := out["flow_id"]; got != flowID {
		t.Errorf("response flow_id = %v, want %s", got, flowID)
	}
}

func TestPatchNumberClearFlowUnassigns(t *testing.T) {
	f := dbtest.Setup(t)
	flowID := seedFlow(t, f, "to-clear")
	numID := insertInboundWithFlowForHandlerTest(t, f, "+918003334444", flowID)
	s := newTestServer(f)

	req := authedRequest(t, s, f, "PATCH", "/v1/numbers/"+numID, `{"clear_flow":true}`)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if got := out["flow_id"]; got != nil {
		t.Errorf("response flow_id = %v, want nil after clear_flow", got)
	}
}

// insertInboundForHandlerTest seeds an unrelated inbound number so the
// patch tests have something to target. Mirrors the outbound helper
// above but skips the trunk/priority fluff.
func insertInboundForHandlerTest(t *testing.T, f *dbtest.Fixture, e164 string) string {
	t.Helper()
	var id string
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, priority, active)
		VALUES ($1, $2, 'inbound', 'generic', 100, true)
		RETURNING id
	`, f.TenantID, e164).Scan(&id)
	if err != nil {
		t.Fatalf("insert inbound: %v", err)
	}
	return id
}

func insertInboundWithFlowForHandlerTest(t *testing.T, f *dbtest.Fixture, e164, flowID string) string {
	t.Helper()
	var id string
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, provider, priority, active, flow_id)
		VALUES ($1, $2, 'inbound', 'generic', 100, true, $3)
		RETURNING id
	`, f.TenantID, e164, flowID).Scan(&id)
	if err != nil {
		t.Fatalf("insert inbound: %v", err)
	}
	return id
}

// TestAuthRequiredRejectsMissingToken covers the auth wall every
// tenant-scoped request flows through.
func TestAuthRequiredRejectsMissingToken(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	req := httptest.NewRequest("GET", "/v1/numbers", nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token got %d, want 401", rec.Code)
	}
}

func TestAuthRequiredRejectsBadToken(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	req := httptest.NewRequest("GET", "/v1/numbers", nil)
	req.Header.Set("Authorization", "Bearer not.a.real.jwt")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token got %d, want 401", rec.Code)
	}
}

// Sanity check: with a freshly-minted console JWT the listing endpoint
// responds 200 and the body is a JSON array (`[]` when empty, not `null`).
func TestListNumbersWithValidToken(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	req := authedRequest(t, s, f, "GET", "/v1/numbers", "")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.HasPrefix(body, "[") {
		t.Errorf("body should be JSON array, got %q", body)
	}
}

// ─── editing e164 / type / trunk, and delete ────────────────────────
//
// A number registered with the wrong digits, type or trunk is corrected in
// place; each edit is held to the same rules as create.

func seedOwnTrunk(t *testing.T, f *dbtest.Fixture, name string) string {
	t.Helper()
	var id string
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO trunks (tenant_id, name, provider_type, register, active)
		VALUES ($1, $2, 'generic', false, true) RETURNING id
	`, f.TenantID, name).Scan(&id); err != nil {
		t.Fatalf("insert trunk: %v", err)
	}
	return id
}

func TestPatchNumberEditsE164TypeAndTrunk(t *testing.T) {
	f := dbtest.Setup(t)
	trunk := seedOwnTrunk(t, f, "tata")
	numID := insertInboundForHandlerTest(t, f, "02265915000")
	s := newTestServer(f)

	rec := serve(t, s, f, "PATCH", "/v1/numbers/"+numID,
		`{"e164":"+912265915000","type":"outbound","trunk_id":"`+trunk+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["e164"] != "+912265915000" || out["type"] != "outbound" || out["trunk_id"] != trunk {
		t.Errorf("returned row = %v, want the edited e164/type/trunk", out)
	}
}

func TestPatchNumberEditRejections(t *testing.T) {
	f := dbtest.Setup(t)
	other, otherTrunk, _ := otherTenant(t, f)
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, active) VALUES ($1, '+919800000010', 'inbound', true)
	`, other); err != nil {
		t.Fatalf("seed: %v", err)
	}
	trunk := seedOwnTrunk(t, f, "own")
	inbound := insertInboundForHandlerTest(t, f, "+919800000011")
	insertInboundForHandlerTest(t, f, "+919800000012")
	var outbound string
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, trunk_id, active)
		VALUES ($1, '+919800000013', 'outbound', $2, true) RETURNING id
	`, f.TenantID, trunk).Scan(&outbound); err != nil {
		t.Fatalf("seed outbound: %v", err)
	}
	s := newTestServer(f)

	cases := []struct {
		name, id, body string
		want           int
	}{
		{"bad e164", inbound, `{"e164":"not-a-number"}`, http.StatusBadRequest},
		{"bad type", inbound, `{"type":"sideways"}`, http.StatusBadRequest},
		{"outbound without trunk", inbound, `{"type":"outbound"}`, http.StatusBadRequest},
		{"foreign trunk", inbound, `{"trunk_id":"` + otherTrunk.String() + `"}`, http.StatusBadRequest},
		{"inbound DID held elsewhere", inbound, `{"e164":"+919800000010"}`, http.StatusConflict},
		{"duplicate in tenant", inbound, `{"e164":"+919800000012"}`, http.StatusConflict},
		{"last outbound changed to inbound", outbound, `{"type":"inbound"}`, http.StatusConflict},
		{"unknown number", uuid.NewString(), `{"type":"masking"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := serve(t, s, f, "PATCH", "/v1/numbers/"+c.id, c.body); rec.Code != c.want {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// Delete is the explicit way out of the last-outbound guardrail, so it is
// allowed even for the only active outbound number.
func TestDeleteNumber(t *testing.T) {
	f := dbtest.Setup(t)
	id := insertOutboundForHandlerTest(t, f, "+918001234567", true)
	s := newTestServer(f)

	if rec := serve(t, s, f, "DELETE", "/v1/numbers/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, s, f, "DELETE", "/v1/numbers/"+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
}

func TestDeleteNumberOfAnotherTenantIsNotFound(t *testing.T) {
	f := dbtest.Setup(t)
	other, _, _ := otherTenant(t, f)
	var id string
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO numbers (tenant_id, e164, type, active) VALUES ($1, '+919800000020', 'inbound', true) RETURNING id
	`, other).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestServer(f)

	if rec := serve(t, s, f, "DELETE", "/v1/numbers/"+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
	var n int
	_ = f.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM numbers WHERE id = $1`, id).Scan(&n)
	if n != 1 {
		t.Fatal("another tenant's number was deleted")
	}
}
