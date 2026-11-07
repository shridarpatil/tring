package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// insertTeamCall inserts a live, answered call with a media room, answered by
// agent and attributed to team ("" stores NULL), with its recording on disk in
// dir so a refused recording can only come from the team gate.
func insertTeamCall(t *testing.T, f *dbtest.Fixture, dir string, agent uuid.UUID, team string) uuid.UUID {
	t.Helper()
	leg := uuid.NewString()
	var id uuid.UUID
	err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, source,
			room_id, agent_id, answered_at, team, recording_url)
		VALUES ($1, 'inbound', 'answered', '+919800000001', 'Support', $2, 'web',
			$5, $3, now(), NULLIF($4, ''), '/v1/calls/x/recording')
		RETURNING id
	`, f.TenantID, leg, agent, team, f.TenantID.String()+"/room-"+leg).Scan(&id)
	if err != nil {
		t.Fatalf("insert call: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, leg+".wav"), []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func newMyTeamsServer(f *dbtest.Fixture, dir string) *Server {
	return NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test", RecordingsDir: dir})
}

func TestManagerCannotSeeOtherTeamCall(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	m := f.AddUser(t, "agent", "m@example.com")
	z := f.AddUser(t, "agent", "z@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{m: true, z: false})
	f.AddTeam(t, "B", map[uuid.UUID]bool{z: false})
	tok := tokenFor(t, s, f, m, "m@example.com", "agent")

	// Z answered a B call: Z being in A too must not expose it to A's manager.
	callB := insertTeamCall(t, f, dir, z, "B")
	if rec := doReq(t, s.Router(), "POST", "/v1/calls/"+callB.String()+"/supervise", tok, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("supervise B call: %d %s, want 403", rec.Code, rec.Body)
	}
	for _, team := range []string{"A", "B"} {
		rec := doReq(t, s.Router(), "GET", "/v1/my-teams/"+team+"/calls/"+callB.String()+"/recording", tok, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("recording of B call via %s: %d %s, want 403", team, rec.Code, rec.Body)
		}
	}

	callA := insertTeamCall(t, f, dir, z, "A")
	if rec := doReq(t, s.Router(), "POST", "/v1/calls/"+callA.String()+"/supervise", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("supervise A call: %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A/calls/"+callA.String()+"/recording", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("recording of A call: %d %s, want 200", rec.Code, rec.Body)
	}

	// A call that waited in A's queue belongs to A even when last rung via B.
	queued := insertTeamCall(t, f, dir, z, "B")
	if _, err := f.DB.Pool.Exec(context.Background(),
		`INSERT INTO queue_stints (tenant_id, call_id, team) VALUES ($1, $2, 'A')`, f.TenantID, queued); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, s.Router(), "POST", "/v1/calls/"+queued.String()+"/supervise", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("supervise call queued in A: %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestSupervisorSupervisesAnyLiveCall(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	sup := f.AddUser(t, "supervisor", "s@example.com")
	agent := f.AddUser(t, "agent", "a@example.com")
	call := insertTeamCall(t, f, dir, agent, "") // no team (e.g. outbound)

	if rec := doReq(t, s.Router(), "POST", "/v1/calls/"+call.String()+"/supervise",
		tokenFor(t, s, f, sup, "s@example.com", "supervisor"), ""); rec.Code != http.StatusOK {
		t.Fatalf("supervisor: %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "POST", "/v1/calls/"+call.String()+"/supervise",
		tokenFor(t, s, f, agent, "a@example.com", "agent"), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("agent: %d %s, want 403", rec.Code, rec.Body)
	}
}

func TestManagerMembership(t *testing.T) {
	f := dbtest.Setup(t)
	s := newMyTeamsServer(f, t.TempDir())
	m := f.AddUser(t, "agent", "m@example.com")
	u := f.AddUser(t, "agent", "u@example.com")
	sup := f.AddUser(t, "supervisor", "s@example.com")
	n := f.AddUser(t, "agent", "n@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{m: true, n: true})
	f.AddTeam(t, "B", map[uuid.UUID]bool{m: false})
	mtok := tokenFor(t, s, f, m, "m@example.com", "agent")
	body := `{"user_id":"` + u.String() + `"}`

	if rec := doReq(t, s.Router(), "POST", "/v1/my-teams/A/members", mtok, body); rec.Code != http.StatusNoContent {
		t.Fatalf("add to A: %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "POST", "/v1/my-teams/A/members", mtok, `{"user_id":"`+uuid.NewString()+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("add unknown user: %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "DELETE", "/v1/my-teams/A/members/"+u.String(), mtok, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove from A: %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "POST", "/v1/my-teams/B/members", mtok, body); rec.Code != http.StatusForbidden {
		t.Fatalf("add to B: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "DELETE", "/v1/my-teams/B/members/"+m.String(), mtok, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("remove from B: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "PUT", "/v1/my-teams/A/members/"+m.String()+"/manager", mtok, `{"is_manager":false}`); rec.Code != http.StatusForbidden {
		t.Fatalf("manager toggles manager: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "DELETE", "/v1/my-teams/A/members/"+uuid.NewString(), mtok, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("remove non-member: %d %s, want 404", rec.Code, rec.Body)
	}

	// Removing a co-manager (or oneself) would strip the manager flag on re-add.
	isManager := func(team string, id uuid.UUID) bool {
		t.Helper()
		var v bool
		if err := f.DB.Pool.QueryRow(context.Background(),
			`SELECT is_manager FROM team_agents WHERE tenant_id = $1 AND team_name = $2 AND agent_id = $3`,
			f.TenantID, team, id).Scan(&v); err != nil {
			t.Fatalf("read is_manager: %v", err)
		}
		return v
	}
	for _, target := range []uuid.UUID{n, m} {
		if rec := doReq(t, s.Router(), "DELETE", "/v1/my-teams/A/members/"+target.String(), mtok, ""); rec.Code != http.StatusForbidden {
			t.Fatalf("manager removes manager %s: %d %s, want 403", target, rec.Code, rec.Body)
		}
		if !isManager("A", target) {
			t.Fatalf("%s lost manager flag", target)
		}
	}

	stok := tokenFor(t, s, f, sup, "s@example.com", "supervisor")
	if rec := doReq(t, s.Router(), "DELETE", "/v1/my-teams/A/members/"+n.String(), stok, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("supervisor removes manager: %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := doReq(t, s.Router(), "PUT", "/v1/my-teams/B/members/"+m.String()+"/manager", stok, `{"is_manager":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("supervisor toggles manager: %d %s, want 204", rec.Code, rec.Body)
	}
	var isMgr bool
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT is_manager FROM team_agents WHERE tenant_id = $1 AND team_name = 'B' AND agent_id = $2`,
		f.TenantID, m).Scan(&isMgr); err != nil || !isMgr {
		t.Fatalf("is_manager = %v (err %v), want true", isMgr, err)
	}
	if rec := doReq(t, s.Router(), "PUT", "/v1/my-teams/B/members/"+u.String()+"/manager", stok, `{"is_manager":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("toggle non-member: %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestMyTeamsVisibility(t *testing.T) {
	f := dbtest.Setup(t)
	s := newMyTeamsServer(f, t.TempDir())
	a := f.AddUser(t, "agent", "a@example.com")
	b := f.AddUser(t, "agent", "b@example.com")
	sup := f.AddUser(t, "supervisor", "s@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{a: false})
	f.AddTeam(t, "B", map[uuid.UUID]bool{b: true})
	f.AddTeam(t, "C", nil) // no members: members must still be []
	atok := tokenFor(t, s, f, a, "a@example.com", "agent")
	stok := tokenFor(t, s, f, sup, "s@example.com", "supervisor")

	list := func(tok string) []map[string]json.RawMessage {
		t.Helper()
		rec := doReq(t, s.Router(), "GET", "/v1/my-teams", tok, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		var out []map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := list(atok)
	if len(got) != 1 || string(got[0]["name"]) != `"A"` {
		t.Fatalf("agent teams = %v, want only A", got)
	}
	var members []myTeamMember
	if err := json.Unmarshal(got[0]["members"], &members); err != nil || len(members) != 1 || members[0].UserID != a {
		t.Fatalf("A members = %s (err %v)", got[0]["members"], err)
	}
	if _, ok := got[0]["queue"]; !ok {
		t.Fatalf("A has no queue: %v", got[0])
	}

	rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A", atok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent get A: %d %s", rec.Code, rec.Body)
	}
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail["stats"]; ok {
		t.Fatalf("agent sees stats: %s", rec.Body)
	}
	if _, ok := detail["calls"]; ok {
		t.Fatalf("agent sees calls: %s", rec.Body)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/my-teams/B", atok, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("agent get B: %d %s, want 404", rec.Code, rec.Body)
	}

	got = list(stok)
	if len(got) != 3 {
		t.Fatalf("supervisor teams = %d, want 3", len(got))
	}
	if string(got[2]["name"]) != `"C"` || string(got[2]["members"]) != "[]" {
		t.Fatalf("C = %v, want members []", got[2])
	}

	seedCall(t, f, b, true, todayAt)
	rec = doReq(t, s.Router(), "GET", "/v1/my-teams/B", stok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("supervisor get B: %d %s", rec.Code, rec.Body)
	}
	detail = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail["stats"]; !ok {
		t.Fatalf("supervisor has no stats: %s", rec.Body)
	}
	// The seeded call has no team or stint, so it isn't B's: calls is [].
	if string(detail["calls"]) != "[]" {
		t.Fatalf("calls = %s, want []", detail["calls"])
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/my-teams/nope", stok, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("supervisor get missing team: %d, want 404", rec.Code)
	}
}

// TestManagerTeamCallsAndStats: a manager's team view lists today's calls that
// belong to the team (by calls.team or a queue stint), with recording links on
// the team route, and counts today's stints.
func TestManagerTeamCallsAndStats(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	m := f.AddUser(t, "agent", "m@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{m: true})
	f.AddTeam(t, "B", nil)
	ctx := context.Background()

	inA := insertTeamCall(t, f, dir, m, "A")
	queuedA := insertTeamCall(t, f, dir, m, "B")
	insertTeamCall(t, f, dir, m, "B") // B's only: hidden
	for _, st := range []struct {
		call    uuid.UUID
		outcome string
	}{{inA, "answered"}, {queuedA, "abandoned"}} {
		if _, err := f.DB.Pool.Exec(ctx, `
			INSERT INTO queue_stints (tenant_id, call_id, team, enqueued_at, ended_at, outcome)
			VALUES ($1, $2, 'A', now() - interval '10 seconds', now(), $3)`, f.TenantID, st.call, st.outcome); err != nil {
			t.Fatal(err)
		}
	}

	rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A", tokenFor(t, s, f, m, "m@example.com", "agent"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get A: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		IsManager bool       `json:"is_manager"`
		Stats     *teamStats `json:"stats"`
		Calls     []Call     `json:"calls"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.IsManager || out.Stats == nil {
		t.Fatalf("out = %s", rec.Body)
	}
	if out.Stats.Offered != 2 || out.Stats.Answered != 1 || out.Stats.Abandoned != 1 || out.Stats.AvgWaitSeconds < 9 {
		t.Fatalf("stats = %+v", *out.Stats)
	}
	ids := map[uuid.UUID]bool{}
	for _, c := range out.Calls {
		ids[c.ID] = true
		if want := "/v1/my-teams/A/calls/" + c.ID.String() + "/recording"; c.RecordingURL == nil || *c.RecordingURL != want {
			t.Fatalf("recording_url = %v, want %s", c.RecordingURL, want)
		}
	}
	if len(out.Calls) != 2 || !ids[inA] || !ids[queuedA] {
		t.Fatalf("calls = %v, want %s and %s", ids, inA, queuedA)
	}
}

func TestCandidatesRequiresManager(t *testing.T) {
	f := dbtest.Setup(t)
	s := newMyTeamsServer(f, t.TempDir())
	mgr := f.AddUser(t, "agent", "m@example.com")
	member := f.AddUser(t, "agent", "a@example.com")
	outsider := f.AddUser(t, "agent", "o@example.com")
	sup := f.AddUser(t, "supervisor", "s@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{mgr: true, member: false})
	atok := tokenFor(t, s, f, member, "a@example.com", "agent")
	mtok := tokenFor(t, s, f, mgr, "m@example.com", "agent")
	stok := tokenFor(t, s, f, sup, "s@example.com", "supervisor")

	if rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A/candidates", atok, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("member: %d %s, want 403", rec.Code, rec.Body)
	}
	rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A/candidates", mtok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("manager: %d %s, want 200", rec.Code, rec.Body)
	}
	var got []struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ids := map[uuid.UUID]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	if !ids[outsider] || !ids[sup] || ids[mgr] || ids[member] {
		t.Fatalf("candidates = %v, want outsider+supervisor, no members", got)
	}
	if rec := doReq(t, s.Router(), "GET", "/v1/my-teams/A/candidates", stok, ""); rec.Code != http.StatusOK {
		t.Fatalf("supervisor: %d %s, want 200", rec.Code, rec.Body)
	}
}
