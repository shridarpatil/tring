package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

var labelPaths = []string{"/v1/dispositions", "/v1/call-tags"}

func labelList(t *testing.T, s *Server, f *dbtest.Fixture, path string) []labelItem {
	t.Helper()
	rec := serve(t, s, f, "GET", path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	var items []labelItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDispositionCRUD(t *testing.T) {
	for _, path := range labelPaths {
		t.Run(path, func(t *testing.T) {
			f := dbtest.Setup(t)
			s := newTestServer(f)

			rec := serve(t, s, f, "POST", path, `{"name":"Resolved"}`)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
			}
			var it labelItem
			if err := json.Unmarshal(rec.Body.Bytes(), &it); err != nil {
				t.Fatal(err)
			}
			if it.Name != "Resolved" || it.Archived {
				t.Fatalf("created = %+v", it)
			}
			if rec := serve(t, s, f, "POST", path, `{"name":"resolved"}`); rec.Code != http.StatusConflict {
				t.Fatalf("duplicate = %d, want 409", rec.Code)
			}

			rec = serve(t, s, f, "PATCH", path+"/"+it.ID.String(), `{"name":"Fixed"}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Fixed"`) {
				t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
			}

			if rec := serve(t, s, f, "PATCH", path+"/"+it.ID.String(), `{"archived":true}`); rec.Code != http.StatusOK {
				t.Fatalf("archive = %d", rec.Code)
			}
			if got := labelList(t, s, f, path); len(got) != 0 {
				t.Fatalf("archived row listed: %+v", got)
			}
			got := labelList(t, s, f, path+"?include_archived=1")
			if len(got) != 1 || !got[0].Archived {
				t.Fatalf("include_archived = %+v", got)
			}

			if rec := serve(t, s, f, "PATCH", path+"/"+it.ID.String(), `{"archived":false}`); rec.Code != http.StatusOK {
				t.Fatalf("restore = %d", rec.Code)
			}
			if got := labelList(t, s, f, path); len(got) != 1 || got[0].Archived {
				t.Fatalf("restored list = %+v", got)
			}
		})
	}
}

func TestDispositionNameRequired(t *testing.T) {
	for _, path := range labelPaths {
		t.Run(path, func(t *testing.T) {
			f := dbtest.Setup(t)
			s := newTestServer(f)
			for _, name := range []string{"", "   ", strings.Repeat("x", 65)} {
				if rec := serve(t, s, f, "POST", path, `{"name":"`+name+`"}`); rec.Code != http.StatusBadRequest {
					t.Errorf("name %q = %d, want 400", name, rec.Code)
				}
			}
		})
	}
}

func TestDispositionScopes(t *testing.T) {
	for _, path := range labelPaths {
		t.Run(path, func(t *testing.T) {
			f := dbtest.Setup(t)
			s := newTestServer(f)
			rec := serve(t, s, f, "POST", "/v1/api-keys", `{"name":"k","scope":"calls"}`)
			var k apiKeyResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
				t.Fatal(err)
			}
			do := func(method, body string) int {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+k.Key)
				w := httptest.NewRecorder()
				s.Router().ServeHTTP(w, r)
				return w.Code
			}
			if got := do("GET", ""); got != http.StatusOK {
				t.Errorf("calls key GET = %d, want 200", got)
			}
			if got := do("POST", `{"name":"x"}`); got != http.StatusForbidden {
				t.Errorf("calls key POST = %d, want 403", got)
			}
		})
	}
}

func TestDispositionCrossTenant(t *testing.T) {
	for i, path := range labelPaths {
		t.Run(path, func(t *testing.T) {
			f := dbtest.Setup(t)
			s := newTestServer(f)
			tenant, _, _ := otherTenant(t, f)
			table := []string{"dispositions", "call_tags"}[i]
			var id string
			if err := f.DB.Pool.QueryRow(t.Context(),
				`INSERT INTO `+table+` (tenant_id, name) VALUES ($1, 'theirs') RETURNING id::text`, tenant).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if rec := serve(t, s, f, "PATCH", path+"/"+id, `{"name":"mine"}`); rec.Code != http.StatusNotFound {
				t.Fatalf("cross-tenant PATCH = %d, want 404", rec.Code)
			}
		})
	}
}

// Labels are shared (teams []) or limited to chosen teams; PATCH replaces the
// set only when teams is present.
func TestLabelTeams(t *testing.T) {
	for _, path := range labelPaths {
		t.Run(path, func(t *testing.T) {
			f := dbtest.Setup(t)
			s := newTestServer(f)
			f.AddTeam(t, "Sales", nil)
			f.AddTeam(t, "Support", nil)

			rec := serve(t, s, f, "POST", path, `{"name":"Shared"}`)
			var shared labelItem
			if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &shared) != nil || shared.Teams == nil || len(shared.Teams) != 0 {
				t.Fatalf("create shared = %d: %s", rec.Code, rec.Body.String())
			}
			rec = serve(t, s, f, "POST", path, `{"name":"Limited","teams":["Support","Sales","Sales"]}`)
			var lim labelItem
			if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &lim) != nil || strings.Join(lim.Teams, ",") != "Sales,Support" {
				t.Fatalf("create limited = %d: %s", rec.Code, rec.Body.String())
			}
			rec = serve(t, s, f, "POST", path, `{"name":"Nope","teams":["Ghost"]}`)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown team: Ghost") {
				t.Fatalf("unknown team create = %d: %s", rec.Code, rec.Body.String())
			}
			// Another tenant's team name is unknown here.
			other, _, _ := otherTenant(t, f)
			if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO teams (tenant_id, name) VALUES ($1, 'Theirs')`, other); err != nil {
				t.Fatal(err)
			}
			rec = serve(t, s, f, "POST", path, `{"name":"Cross","teams":["Theirs"]}`)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown team: Theirs") {
				t.Fatalf("other tenant's team = %d: %s", rec.Code, rec.Body.String())
			}
			// A blank team name is rejected, never dropped (that would make a
			// meant-to-be-limited label shared).
			rec = serve(t, s, f, "POST", path, `{"name":"Blank","teams":[" ",""]}`)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "team name is blank") {
				t.Fatalf("blank teams create = %d: %s", rec.Code, rec.Body.String())
			}
			rec = serve(t, s, f, "PATCH", path+"/"+lim.ID.String(), `{"teams":[" "]}`)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "team name is blank") {
				t.Fatalf("blank teams patch = %d: %s", rec.Code, rec.Body.String())
			}
			got := labelList(t, s, f, path)
			if len(got) != 2 {
				t.Fatalf("list after rejected requests = %+v", got)
			}
			for _, it := range got {
				if it.ID == lim.ID && strings.Join(it.Teams, ",") != "Sales,Support" {
					t.Fatalf("rejected blank patch changed teams: %+v", it)
				}
			}

			// Rename without teams keeps them; teams present replaces.
			rec = serve(t, s, f, "PATCH", path+"/"+lim.ID.String(), `{"name":"Limited 2"}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"teams":["Sales","Support"]`) {
				t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
			}
			rec = serve(t, s, f, "PATCH", path+"/"+lim.ID.String(), `{"teams":["Ghost"]}`)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown team: Ghost") {
				t.Fatalf("unknown team patch = %d: %s", rec.Code, rec.Body.String())
			}
			rec = serve(t, s, f, "PATCH", path+"/"+shared.ID.String(), `{"teams":["Support"]}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"teams":["Support"]`) {
				t.Fatalf("limit shared = %d: %s", rec.Code, rec.Body.String())
			}
			rec = serve(t, s, f, "PATCH", path+"/"+lim.ID.String(), `{"teams":[]}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"teams":[]`) {
				t.Fatalf("share limited = %d: %s", rec.Code, rec.Body.String())
			}
			got = labelList(t, s, f, path)
			teams := map[string]string{}
			for _, it := range got {
				teams[it.Name] = strings.Join(it.Teams, ",")
			}
			if len(got) != 2 || teams["Limited 2"] != "" || teams["Shared"] != "Support" {
				t.Fatalf("list = %+v", got)
			}
		})
	}
}

// Deleting a team archives the labels limited to it alone (they would
// otherwise silently become shared); labels also on another team keep it.
func TestTeamDeleteArchivesTeamOnlyLabels(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	f.AddTeam(t, "A", nil)
	f.AddTeam(t, "B", nil)
	mk := func(path, body string) labelItem {
		rec := serve(t, s, f, "POST", path, body)
		var it labelItem
		if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &it) != nil {
			t.Fatalf("create %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		return it
	}
	mk("/v1/dispositions", `{"name":"A only","teams":["A"]}`)
	mk("/v1/dispositions", `{"name":"A and B","teams":["A","B"]}`)
	mk("/v1/dispositions", `{"name":"Shared"}`)
	mk("/v1/call-tags", `{"name":"a-tag","teams":["A"]}`)

	if rec := serve(t, s, f, "DELETE", "/v1/teams/A", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete team = %d: %s", rec.Code, rec.Body.String())
	}
	got := map[string]labelItem{}
	for _, it := range labelList(t, s, f, "/v1/dispositions?include_archived=1") {
		got[it.Name] = it
	}
	if it := got["A only"]; !it.Archived || len(it.Teams) != 0 {
		t.Fatalf("A only = %+v, want archived", it)
	}
	if it := got["A and B"]; it.Archived || strings.Join(it.Teams, ",") != "B" {
		t.Fatalf("A and B = %+v, want live with [B]", it)
	}
	if it := got["Shared"]; it.Archived {
		t.Fatalf("Shared archived: %+v", it)
	}
	if tags := labelList(t, s, f, "/v1/call-tags?include_archived=1"); len(tags) != 1 || !tags[0].Archived {
		t.Fatalf("tags = %+v, want a-tag archived", tags)
	}
}
