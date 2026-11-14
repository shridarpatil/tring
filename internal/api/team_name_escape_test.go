package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// A team name with a reserved character arrives percent-encoded (R&D →
// R%26D). Go keeps a RawPath for such URLs and chi matches on it, so the
// {name} param stayed "R%26D" and the team was never found.
func TestTeamRoutesUnescapeName(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	teamCall(t, s, f, http.MethodPost, "/v1/teams", `{"name":"R&D"}`, http.StatusCreated)
	teamCall(t, s, f, http.MethodGet, "/v1/teams/R%26D", "", http.StatusOK)
	teamCall(t, s, f, http.MethodPatch, "/v1/teams/R%26D", `{"sla_seconds":60}`, http.StatusOK)
	// A name whose escaping is canonical (no RawPath) must keep working, and a
	// literal % must not be decoded twice.
	teamCall(t, s, f, http.MethodPost, "/v1/teams", `{"name":"Sales 100%"}`, http.StatusCreated)
	teamCall(t, s, f, http.MethodGet, "/v1/teams/Sales%20100%25", "", http.StatusOK)

	m := f.AddUser(t, "agent", "m@example.com")
	f.AddTeam(t, "Q&A", map[uuid.UUID]bool{m: true})
	tok := tokenFor(t, s, f, m, "m@example.com", "agent")
	if rec := doReq(t, s.Router(), "GET", "/v1/my-teams/Q%26A", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("my-teams Q%%26A: %d %s", rec.Code, rec.Body)
	}

	if rec := serve(t, s, f, http.MethodDelete, "/v1/teams/R%26D", ""); rec.Code >= 300 {
		t.Fatalf("delete R%%26D: %d %s", rec.Code, rec.Body)
	}
}
