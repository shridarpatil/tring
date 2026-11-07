package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/presence"
)

type renameResponse struct {
	userRow
	FlowsReferencingOld []flowRef `json:"flows_referencing_old"`
}

// createAgentUser adds a user with the given SIP username and returns its id.
func createAgentUser(t *testing.T, h http.Handler, admin, sip string) uuid.UUID {
	t.Helper()
	rec := doReq(t, h, "POST", "/v1/users", admin,
		`{"email":"`+sip+`@example.com","password":"correct-horse","role":"agent","sip_username":"`+sip+`"}`)
	var u userRow
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &u) != nil {
		t.Fatalf("create %s: %d %s", sip, rec.Code, rec.Body)
	}
	return u.ID
}

func TestUsersRenameSip(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	id := createAgentUser(t, h, admin, "bbo")

	var kicked struct {
		path string
		sip  string
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SIP string `json:"sip"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		kicked.path, kicked.sip = r.URL.Path, body.SIP
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gw.Close()
	s.gatewayControlURL = gw.URL

	path := "/v1/users/" + id.String()
	if rec := doReq(t, h, "PATCH", path, admin, `{"sip_username":"bad name!"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid sip = %d, want 400", rec.Code)
	}
	rec := doReq(t, h, "PATCH", path, admin, `{"sip_username":" Bob ","display_name":"Bob"}`)
	var got renameResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if got.SipUsername != "bob" || got.DisplayName != "Bob" {
		t.Errorf("row = %+v, want sip bob / name Bob", got.userRow)
	}
	if kicked.path != "/control/agent-kick" || kicked.sip != "bbo" {
		t.Errorf("gateway kick = %+v, want /control/agent-kick for bbo", kicked)
	}

	// Unchanged name: no rename, no kick.
	kicked.path = ""
	if rec := doReq(t, h, "PATCH", path, admin, `{"sip_username":"bob"}`); rec.Code != http.StatusOK {
		t.Errorf("same sip = %d %s", rec.Code, rec.Body)
	}
	if kicked.path != "" {
		t.Errorf("same sip kicked the agent")
	}

	// A gateway that's down doesn't fail the rename.
	s.gatewayControlURL = "http://127.0.0.1:1"
	if rec := doReq(t, h, "PATCH", path, admin, `{"sip_username":"bob2"}`); rec.Code != http.StatusOK {
		t.Errorf("rename with gateway down = %d %s", rec.Code, rec.Body)
	}
}

func TestUsersRenameSipCollision(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	id := createAgentUser(t, h, admin, "carol")
	createAgentUser(t, h, admin, "dave")

	rec := doReq(t, h, "PATCH", "/v1/users/"+id.String(), admin, `{"sip_username":"dave","display_name":"Carol"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("collision = %d %s, want 409", rec.Code, rec.Body)
	}
	// The rejected request changes nothing, its other fields included.
	u, err := s.getUserRow(context.Background(), f.TenantID, id)
	if err != nil || u.SipUsername != "carol" || u.DisplayName == "Carol" {
		t.Errorf("after 409 row = %+v (%v)", u, err)
	}
}

func TestUsersRenameSipOnCall(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	id := createAgentUser(t, h, admin, "erin")
	if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, agent_id)
		VALUES ($1, 'inbound', 'answered', '+919800000001', '+919800000089', 'leg-oncall', $2)`, f.TenantID, id); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, h, "PATCH", "/v1/users/"+id.String(), admin, `{"sip_username":"erin2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("on call = %d %s, want 409", rec.Code, rec.Body)
	}
	if u, _ := s.getUserRow(context.Background(), f.TenantID, id); u.SipUsername != "erin" {
		t.Errorf("sip = %q after refused rename", u.SipUsername)
	}
}

func TestUsersRenameSipReportsFlows(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	id := createAgentUser(t, h, admin, "frank")

	var pinned uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO flows (tenant_id, name, definition) VALUES ($1, 'pinned',
		'{"version":2,"nodes":[{"id":"t","type":"transfer","config":{"agent_sip":"frank"}}],"edges":[],"entry_node":"t"}')
		RETURNING id`, f.TenantID).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO flows (tenant_id, name, definition) VALUES ($1, 'other',
		'{"version":2,"nodes":[{"id":"t","type":"transfer","config":{"agent_sip":"frankie"}},{"id":"p","type":"play","config":{"text":"frank"}}],"edges":[],"entry_node":"t"}')`,
		f.TenantID); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, h, "PATCH", "/v1/users/"+id.String(), admin, `{"sip_username":"frank.k"}`)
	var got renameResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if len(got.FlowsReferencingOld) != 1 || got.FlowsReferencingOld[0].ID != pinned || got.FlowsReferencingOld[0].Name != "pinned" {
		t.Errorf("flows_referencing_old = %+v, want only pinned", got.FlowsReferencingOld)
	}
}

// With Redis: the gateway's cached agent lookup and the old name's presence
// keys are dropped, and the Away flag follows the agent to the new name.
func TestUsersRenameSipMovesPresence(t *testing.T) {
	f := dbtest.Setup(t)
	s, c := cachedTestServer(t, f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	id := createAgentUser(t, h, admin, "gina")
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE users SET available_for_calls = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	tenant := f.TenantID.String()
	pres := presence.New(c)
	pres.MarkOnline(ctx, tenant, "gina")
	pres.MarkFreed(ctx, tenant, "gina")
	pres.SetAvailability(ctx, tenant, "gina", false)
	c.Set(ctx, callcdr.AgentCacheKey(f.TenantID, "gina"), []byte(`{}`), 0)
	t.Cleanup(func() { pres.Offline(ctx, tenant, "gina2") })

	if rec := doReq(t, h, "PATCH", "/v1/users/"+id.String(), admin, `{"sip_username":"gina2"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if _, ok := c.Get(ctx, callcdr.AgentCacheKey(f.TenantID, "gina")); ok {
		t.Error("old agent cache entry survived")
	}
	if pres.IsOnline(ctx, tenant, "gina") {
		t.Error("old presence marker survived")
	}
	if _, ok := pres.FreedAt(ctx, tenant, "gina"); ok {
		t.Error("old freed stamp survived")
	}
	if _, ok := pres.CachedAvailability(ctx, tenant, "gina"); ok {
		t.Error("old availability survived")
	}
	if avail, ok := pres.CachedAvailability(ctx, tenant, "gina2"); !ok || avail {
		t.Errorf("new availability = %v,%v, want false,true", avail, ok)
	}
}
