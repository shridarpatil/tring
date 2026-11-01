package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

const monHours = `[{"day":"monday","enabled":true,"start_time":"09:00","end_time":"17:00"}]`

type teamResp struct {
	RingSeconds   int             `json:"ring_seconds"`
	MinLiveAgents int             `json:"min_live_agents"`
	SLASeconds    int             `json:"sla_seconds"`
	Hours         json.RawMessage `json:"hours"`
}

func teamCall(t *testing.T, s *Server, f *dbtest.Fixture, method, target, body string, wantCode int) teamResp {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, authedRequest(t, s, f, method, target, body))
	if rec.Code != wantCode {
		t.Fatalf("%s %s = %d, want %d: %s", method, target, rec.Code, wantCode, rec.Body.String())
	}
	var tr teamResp
	if wantCode < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
			t.Fatalf("decode: %v: %s", err, rec.Body.String())
		}
	}
	return tr
}

func jsonEq(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got %q not JSON: %v", got, err)
	}
	_ = json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("hours = %s, want %s", gb, wb)
	}
}

func TestPatchTeamLiveSettings(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	teamCall(t, s, f, http.MethodPost, "/v1/teams", `{"name":"support"}`, http.StatusCreated)

	body := `{"min_live_agents":10,"sla_seconds":300,"hours":` + monHours + `}`
	check := func(tr teamResp, ring int) {
		t.Helper()
		if tr.MinLiveAgents != 10 || tr.SLASeconds != 300 || tr.RingSeconds != ring {
			t.Errorf("got %+v, want min 10 sla 300 ring %d", tr, ring)
		}
		jsonEq(t, tr.Hours, monHours)
	}
	check(teamCall(t, s, f, http.MethodPatch, "/v1/teams/support", body, http.StatusOK), 20)
	check(teamCall(t, s, f, http.MethodGet, "/v1/teams/support", "", http.StatusOK), 20)
	check(teamCall(t, s, f, http.MethodPatch, "/v1/teams/support", `{"ring_seconds":25}`, http.StatusOK), 25)
	check(teamCall(t, s, f, http.MethodGet, "/v1/teams/support", "", http.StatusOK), 25)
}

func TestCreateTeamDefaultsLiveSettings(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	tr := teamCall(t, s, f, http.MethodPost, "/v1/teams", `{"name":"support"}`, http.StatusCreated)
	if tr.MinLiveAgents != 0 || tr.SLASeconds != 0 {
		t.Errorf("got %+v, want zero min/sla", tr)
	}
	jsonEq(t, tr.Hours, `[]`)
}

func TestPatchTeamRejectsBadHours(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	teamCall(t, s, f, http.MethodPost, "/v1/teams", `{"name":"support"}`, http.StatusCreated)
	for _, h := range []string{
		`{"x":1}`,
		`[{"day":"monday","enabled":true,"start_time":"25:00","end_time":"26:00"}]`,
	} {
		teamCall(t, s, f, http.MethodPatch, "/v1/teams/support", `{"hours":`+h+`}`, http.StatusBadRequest)
		tr := teamCall(t, s, f, http.MethodGet, "/v1/teams/support", "", http.StatusOK)
		jsonEq(t, tr.Hours, `[]`)
	}
}

func TestPatchTeamRejectsNegative(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	teamCall(t, s, f, http.MethodPost, "/v1/teams", `{"name":"support"}`, http.StatusCreated)
	teamCall(t, s, f, http.MethodPatch, "/v1/teams/support", `{"min_live_agents":-1}`, http.StatusBadRequest)
	teamCall(t, s, f, http.MethodPatch, "/v1/teams/support", `{"sla_seconds":-5}`, http.StatusBadRequest)
}
