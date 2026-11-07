package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func TestUsersCreateAndLogin(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")

	rec := doReq(t, h, "POST", "/v1/users", admin,
		`{"email":"Ann@Example.com","password":"correct-horse","role":"agent","display_name":"Ann"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var u userRow
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Role != "agent" || u.SipUsername != "ann@example.com" || !u.CanLogin || u.Teams == nil {
		t.Fatalf("row = %+v", u)
	}

	rec = doReq(t, h, "POST", "/v1/console/login", "", `{"email":"ann@example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var lr consoleLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	if lr.User.Role != "agent" {
		t.Fatalf("login role = %q", lr.User.Role)
	}

	rec = doReq(t, h, "GET", "/v1/users", admin, "")
	var list []userRow
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestUsersAPIKeyForbidden(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()

	rec := doReq(t, h, "POST", "/v1/api-keys", tokenFor(t, s, f, f.UserID, f.UserEmail, "admin"), `{"name":"k","scope":"full"}`)
	var k apiKeyResponse
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &k) != nil {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/users", ""},
		{"POST", "/v1/users", `{"email":"x@example.com","password":"correct-horse","role":"admin"}`},
		{"PATCH", "/v1/users/" + f.UserID.String(), `{"role":"agent"}`},
		{"DELETE", "/v1/users/" + f.UserID.String(), ""},
	} {
		rec := doReq(t, h, c.method, c.path, k.Key, c.body)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with API key = %d", c.method, c.path, rec.Code)
		}
	}
}

func TestUsersAdminCannotDemoteSelf(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	self := "/v1/users/" + f.UserID.String()

	for _, body := range []string{`{"role":"agent"}`, `{"email":""}`} {
		if rec := doReq(t, h, "PATCH", self, admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", body, rec.Code)
		}
	}
	if rec := doReq(t, h, "DELETE", self, admin, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("delete self = %d, want 400", rec.Code)
	}
	// Re-asserting the same role is harmless.
	if rec := doReq(t, h, "PATCH", self, admin, `{"role":"admin","display_name":"Me"}`); rec.Code != http.StatusOK {
		t.Errorf("same role = %d %s", rec.Code, rec.Body)
	}
}

func TestUsersDuplicateEmailAcrossTenants(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")

	var otherTenant uuid.UUID
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO tenants (name, sip_domain, api_key_hash, api_key_prefix)
		VALUES ('other', 'other.tring.local', $1, 'deprecated--') RETURNING id`,
		"deprecated-"+uuid.NewString()).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO users (tenant_id, email, password_hash, sip_username, sip_password, role)
		VALUES ($1, 'dup@example.com', 'x', 'dup-sip', 'x', 'agent')`, otherTenant); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, h, "POST", "/v1/users", admin,
		`{"email":"dup@example.com","password":"correct-horse","role":"agent","sip_username":"mine"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dup email = %d %s, want 409", rec.Code, rec.Body)
	}
}

func TestUsersClearLoginRevokesSession(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	h := s.Router()
	admin := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")

	rec := doReq(t, h, "POST", "/v1/users", admin, `{"email":"bob@example.com","password":"correct-horse","role":"agent"}`)
	var u userRow
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &u) != nil {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	bob := tokenFor(t, s, f, u.ID, "bob@example.com", "agent")
	if rec := doReq(t, h, "GET", "/v1/me", bob, ""); rec.Code != http.StatusOK {
		t.Fatalf("me before: %d", rec.Code)
	}

	rec = doReq(t, h, "PATCH", "/v1/users/"+u.ID.String(), admin, `{"email":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	var cleared userRow
	_ = json.Unmarshal(rec.Body.Bytes(), &cleared)
	if cleared.CanLogin || cleared.Email != "" {
		t.Fatalf("row after clear = %+v", cleared)
	}
	if rec := doReq(t, h, "GET", "/v1/me", bob, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after clear = %d, want 401", rec.Code)
	}

	// Delete removes the (sip-only) user.
	if rec := doReq(t, h, "DELETE", "/v1/users/"+u.ID.String(), admin, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}
