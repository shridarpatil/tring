package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// selfScopeAllowed reports whether a non-admin console session may reach a
// route: principal-checked routes (exact path or sub-path, never a mere string
// prefix) plus the static/SPA surfaces that aren't console-authenticated.
func selfScopeAllowed(route string) bool {
	switch route {
	case "/v1/me", "/v1/my-teams", "/v1/console/login", "/v1/calls/{id}/supervise", "/healthz":
		return true
	}
	for _, p := range []string{"/v1/me/", "/v1/my-teams/", "/sdk/", "/admin", "/login", "/agent"} {
		if strings.HasPrefix(route, p) {
			return true
		}
	}
	return false
}

var routeParam = regexp.MustCompile(`\{[^}]+\}`)

func tokenFor(t *testing.T, s *Server, f *dbtest.Fixture, userID uuid.UUID, email, role string) string {
	t.Helper()
	// Mint at the user's current epoch, as login would.
	var epoch int
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT session_epoch FROM users WHERE id = $1`, userID).Scan(&epoch); err != nil {
		t.Fatalf("read epoch: %v", err)
	}
	tok, _, err := s.IssueConsoleToken(userID, f.TenantID, email, role, epoch, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

func doReq(t *testing.T, h http.Handler, method, path, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	// Short deadline so a long-lived route (SSE) that wrongly lets the caller
	// through can't hang the test.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil).WithContext(ctx)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// assertAllLegacyRoutesDenied walks every registered route and asserts a
// console session for the given role is refused outside the allowlist, so a
// route added later without a principal check fails here by default.
func assertAllLegacyRoutesDenied(t *testing.T, role string) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	email := role + "@example.com"
	tok := tokenFor(t, s, f, f.AddUser(t, role, email), email, role)
	h := s.Router()

	checked := 0
	err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if selfScopeAllowed(route) {
			return nil
		}
		path := routeParam.ReplaceAllString(route, uuid.NewString())
		rec := doReq(t, h, method, path, tok, "")
		// Routes in the AuthRequired group must answer 403 (authenticated,
		// refused); only the agent-JWT /v1/sdk/* routes see a console token as
		// unauthenticated.
		want := http.StatusForbidden
		if strings.HasPrefix(route, "/v1/sdk/") {
			want = http.StatusUnauthorized
		}
		if rec.Code != want {
			t.Errorf("%s session: %s %s = %d, want %d", role, method, route, rec.Code, want)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked < 50 {
		t.Fatalf("walked only %d routes; router walk is broken", checked)
	}
}

func TestNonAdminConsoleDeniedOnLegacyRoutes(t *testing.T) {
	t.Run("agent", func(t *testing.T) { assertAllLegacyRoutesDenied(t, "agent") })
	t.Run("supervisor", func(t *testing.T) { assertAllLegacyRoutesDenied(t, "supervisor") })
}

func TestAdminConsoleKeepsFullAccess(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	tok := tokenFor(t, s, f, f.UserID, f.UserEmail, "admin")
	h := s.Router()
	for _, p := range []string{"/v1/trunks", "/v1/calls"} {
		if rec := doReq(t, h, "GET", p, tok, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200: %s", p, rec.Code, rec.Body)
		}
	}
}

func TestPrincipalReloadedEachRequest(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	b := f.AddUser(t, "admin", "b@example.com")
	// The JWT says admin throughout; only the DB changes.
	tok := tokenFor(t, s, f, b, "b@example.com", "admin")
	h := s.Router()
	ctx := context.Background()

	if rec := doReq(t, h, "GET", "/v1/trunks", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("admin: %d, want 200", rec.Code)
	}
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE users SET role = 'agent' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "GET", "/v1/trunks", tok, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("demoted: %d, want 403", rec.Code)
	}
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE users SET password_hash = NULL WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "GET", "/v1/trunks", tok, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login revoked: %d, want 401", rec.Code)
	}
}

func setPassword(t *testing.T, f *dbtest.Fixture, userID uuid.UUID, pw string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(context.Background(),
		`UPDATE users SET password_hash = $1 WHERE id = $2`, string(hash), userID); err != nil {
		t.Fatal(err)
	}
}

func TestLoginReturnsRoleAndTeams(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := f.AddUser(t, "agent", "a@example.com")
	setPassword(t, f, id, "correct-horse")
	f.AddTeam(t, "sales", map[uuid.UUID]bool{id: true})

	rec := doReq(t, s.Router(), "POST", "/v1/console/login", "",
		`{"email":"a@example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var resp consoleLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.User.Role != "agent" || resp.User.ID != id.String() || resp.User.SipUsername != "a@example.com" {
		t.Fatalf("user = %+v", resp.User)
	}
	if len(resp.User.Teams) != 1 || resp.User.Teams[0] != (myTeamRef{Name: "sales", IsManager: true}) {
		t.Fatalf("teams = %+v", resp.User.Teams)
	}
	claims, err := s.parseConsoleToken(resp.Token)
	if err != nil || claims.Role != "agent" {
		t.Fatalf("token role claim = %v, %v", claims, err)
	}
}

func TestLoginEmailCaseInsensitive(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	id := f.AddUser(t, "agent", "a@example.com")
	setPassword(t, f, id, "correct-horse")

	rec := doReq(t, s.Router(), "POST", "/v1/console/login", "",
		`{"email":"A@Example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	// No teams must serialise as [] so the console can iterate without a guard.
	if !strings.Contains(rec.Body.String(), `"teams":[]`) {
		t.Fatalf("want empty teams array: %s", rec.Body)
	}
}

func TestPrincipalTeamChecks(t *testing.T) {
	agent := &Principal{Role: "agent", Teams: []string{"sales", "support"}, ManagedTeams: []string{"sales"}}
	if agent.IsAdmin() || agent.SeesAllTeams() {
		t.Fatal("agent must not see all teams")
	}
	if !agent.CanViewTeam("support") || agent.CanViewTeam("billing") {
		t.Fatal("CanViewTeam membership")
	}
	if !agent.ManagesTeam("sales") || agent.ManagesTeam("support") {
		t.Fatal("ManagesTeam")
	}
	if all, teams := agent.SupervisableTeams(); all || len(teams) != 1 || teams[0] != "sales" {
		t.Fatalf("SupervisableTeams = %v %v", all, teams)
	}
	sup := &Principal{Role: "supervisor"}
	if sup.IsAdmin() || !sup.SeesAllTeams() || !sup.CanViewTeam("x") || !sup.ManagesTeam("x") {
		t.Fatal("supervisor sees and manages all teams")
	}
	if all, _ := sup.SupervisableTeams(); !all {
		t.Fatal("supervisor supervises all")
	}
	if !(&Principal{Role: "admin"}).IsAdmin() {
		t.Fatal("admin")
	}
}
