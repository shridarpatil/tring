package api

import (
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
