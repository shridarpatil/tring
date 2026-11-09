package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/dbtest"
)

// A console JWT outlives its user unless the middleware re-checks the user
// row: deleting a user must revoke their still-unexpired session.
func TestConsoleTokenForDeletedUserRejected(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	if rec := serve(t, s, f, http.MethodGet, "/v1/agents", ""); rec.Code != http.StatusOK {
		t.Fatalf("live user: status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	// Mint the token while the user exists, then delete the user.
	req := authedRequest(t, s, f, http.MethodGet, "/v1/agents", "")
	superviseReq := authedRequest(t, s, f, http.MethodPost, "/v1/calls/"+f.TenantID.String()+"/supervise", "")
	if _, err := f.DB.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.UserID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	for _, r := range []*http.Request{req, superviseReq} {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with deleted user's token: status = %d, want 401; body %s",
				r.Method, r.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

// noPasswordUser adds an agent with an email and a NULL password hash: the
// shape an SSO-only user has.
func noPasswordUser(t *testing.T, f *dbtest.Fixture, email string) uuid.UUID {
	t.Helper()
	id := f.AddUser(t, "agent", email)
	if _, err := f.DB.Pool.Exec(context.Background(), `UPDATE users SET password_hash = NULL WHERE id = $1`, id); err != nil {
		t.Fatalf("null hash: %v", err)
	}
	return id
}

func TestConsoleLoginNoPasswordUser(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	noPasswordUser(t, f, "sso@example.com")

	rec := doReq(t, s.Router(), "POST", "/v1/console/login", "", `{"email":"sso@example.com","password":"anything-at-all"}`)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Fatalf("login = %d %s, want 401 invalid credentials", rec.Code, rec.Body)
	}
}

func TestPrincipalWithoutPassword(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := noPasswordUser(t, f, "sso@example.com")

	rec := doReq(t, s.Router(), "GET", "/v1/me", tokenFor(t, s, f, id, "sso@example.com", "agent"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestMePasswordNoPasswordSet(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := noPasswordUser(t, f, "sso@example.com")

	rec := doReq(t, s.Router(), "POST", "/v1/me/password", tokenFor(t, s, f, id, "sso@example.com", "agent"),
		`{"current":"x","new":"long-enough-pw"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no password set") {
		t.Fatalf("= %d %s, want 400 no password set", rec.Code, rec.Body)
	}
}

func TestListUsersCanLoginWithoutPassword(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := noPasswordUser(t, f, "sso@example.com")

	rec := doReq(t, s.Router(), "GET", "/v1/users", tokenFor(t, s, f, f.UserID, f.UserEmail, "admin"), "")
	var list []userRow
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	for _, u := range list {
		if u.ID == id {
			if !u.CanLogin {
				t.Fatalf("can_login = false for password-less user with email")
			}
			return
		}
	}
	t.Fatal("user not listed")
}
