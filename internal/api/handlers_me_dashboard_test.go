package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func getMeDashboard(t *testing.T, s *Server, tok string) dashboardSnapshot {
	t.Helper()
	rec := doReq(t, s.Router(), "GET", "/v1/me/dashboard", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me/dashboard: %d %s", rec.Code, rec.Body)
	}
	var snap dashboardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func snapCallIDs(snap dashboardSnapshot) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(snap.Calls))
	for _, c := range snap.Calls {
		ids = append(ids, c.ID)
	}
	return ids
}

func snapTeams(snap dashboardSnapshot) []string {
	out := []string{}
	for _, q := range snap.Queues.Queues {
		out = append(out, q.Team)
	}
	return out
}

func snapAgentIDs(snap dashboardSnapshot) []uuid.UUID {
	out := []uuid.UUID{}
	for _, a := range snap.Agents {
		out = append(out, a.ID)
	}
	return out
}

// seedStint records a finished queue stint for team that waited 5 minutes
// against a 30 s SLA, so it counts as one breach today.
func seedBreachStint(t *testing.T, f *dbtest.Fixture, team string) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(), `
		INSERT INTO queue_stints (tenant_id, team, enqueued_at, ended_at, outcome, sla_seconds)
		VALUES ($1, $2, now() - interval '5 minutes', now(), 'answered', 30)`, f.TenantID, team); err != nil {
		t.Fatalf("seed stint: %v", err)
	}
}

func sameIDs(got, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

func TestMeDashboardAgentScope(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	a := f.AddUser(t, "agent", "a@example.com")
	b := f.AddUser(t, "agent", "b@example.com")
	z := f.AddUser(t, "agent", "z@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{a: false, b: false})
	f.AddTeam(t, "B", map[uuid.UUID]bool{z: false})

	mine := insertTeamCall(t, f, dir, a, "A")
	insertTeamCall(t, f, dir, b, "A") // a teammate's call: its caller is not a's to see
	insertTeamCall(t, f, dir, z, "B")

	// Today: one call a answered (60 s talk), one that rang a and was missed,
	// and one only b handled.
	seedCall(t, f, a, true, todayAt)
	missed := seedCall(t, f, uuid.Nil, false, todayAt)
	seedRing(t, f, missed, a)
	seedCall(t, f, b, true, todayAt)
	seedBreachStint(t, f, "A")
	seedBreachStint(t, f, "B")

	snap := getMeDashboard(t, s, tokenFor(t, s, f, a, "a@example.com", "agent"))

	if got := snapCallIDs(snap); !sameIDs(got, []uuid.UUID{mine}) {
		t.Fatalf("calls = %v, want only %v", got, mine)
	}
	if got := snapTeams(snap); !slices.Equal(got, []string{"A"}) {
		t.Fatalf("queues = %v, want [A]", got)
	}
	if got := snapAgentIDs(snap); !sameIDs(got, []uuid.UUID{a, b}) {
		t.Fatalf("agents = %v, want a and b", got)
	}
	if snap.Queues.OnCall != 1 {
		t.Fatalf("on_call = %d, want 1 (own live call only)", snap.Queues.OnCall)
	}

	st := snap.Stats
	// The live call, the answered one and the missed one.
	if st.CallsToday != 3 || st.AnsweredPct != 67 || st.AbandonedToday != 1 || st.FailedToday != 0 {
		t.Fatalf("stats = %+v, want 3 calls, 67%% answered, 1 missed", st)
	}
	if st.AvgTalkSeconds != 60 {
		t.Fatalf("avg talk = %d, want 60", st.AvgTalkSeconds)
	}
	if st.SLABreachesToday != 1 {
		t.Fatalf("sla breaches = %d, want 1 (team A only)", st.SLABreachesToday)
	}
	breaches := 0
	for _, h := range st.Hourly {
		breaches += h.Breaches
	}
	if len(st.Hourly) != 12 || st.Hourly[11].Count < 1 || breaches != 1 {
		t.Fatalf("hourly = %+v, want 12 buckets, own live call this hour, A's breach only", st.Hourly)
	}
}

func TestMeDashboardManagerScope(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	m := f.AddUser(t, "agent", "m@example.com")
	z := f.AddUser(t, "agent", "z@example.com")
	y := f.AddUser(t, "agent", "y@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{m: true, z: false})
	f.AddTeam(t, "B", map[uuid.UUID]bool{z: false})
	f.AddTeam(t, "C", map[uuid.UUID]bool{m: false, y: false}) // member, not manager

	callA := insertTeamCall(t, f, dir, z, "A")
	insertTeamCall(t, f, dir, z, "B") // z is in A, but the call is B's
	insertTeamCall(t, f, dir, y, "C") // m only belongs to C
	queued := insertTeamCall(t, f, dir, z, "B")
	if _, err := f.DB.Pool.Exec(context.Background(),
		`INSERT INTO queue_stints (tenant_id, call_id, team) VALUES ($1, $2, 'A')`, f.TenantID, queued); err != nil {
		t.Fatal(err)
	}
	own := insertTeamCall(t, f, dir, m, "")

	snap := getMeDashboard(t, s, tokenFor(t, s, f, m, "m@example.com", "agent"))

	if got := snapCallIDs(snap); !sameIDs(got, []uuid.UUID{callA, queued, own}) {
		t.Fatalf("calls = %v, want A's, the one queued in A, and own", got)
	}
	if got := snapTeams(snap); !slices.Equal(got, []string{"A"}) {
		t.Fatalf("queues = %v, want [A] (managed only)", got)
	}
	if got := snapAgentIDs(snap); !sameIDs(got, []uuid.UUID{m, z}) {
		t.Fatalf("agents = %v, want m and z", got)
	}
}

// A supervisor's /v1/me/dashboard is the tenant-wide snapshot, as the stream's.
func TestMeDashboardSupervisorIsTenantWide(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	sup := f.AddUser(t, "supervisor", "s@example.com")
	z := f.AddUser(t, "agent", "z@example.com")
	f.AddTeam(t, "B", map[uuid.UUID]bool{z: false})
	call := insertTeamCall(t, f, dir, z, "B")

	snap := getMeDashboard(t, s, tokenFor(t, s, f, sup, "s@example.com", "supervisor"))
	if got := snapCallIDs(snap); !sameIDs(got, []uuid.UUID{call}) {
		t.Fatalf("calls = %v, want %v", got, call)
	}
	if got := snapTeams(snap); !slices.Equal(got, []string{"B"}) {
		t.Fatalf("queues = %v, want [B]", got)
	}
}

func TestSupervisorGetsDashboardStream(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	sup := f.AddUser(t, "supervisor", "s@example.com")
	tok := tokenFor(t, s, f, sup, "s@example.com", "supervisor")

	// The stream runs until the client leaves; a short deadline ends it.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "/v1/stream/dashboard", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event: snapshot") {
		t.Fatalf("supervisor stream: %d %s", rec.Code, rec.Body)
	}
}

// stats=0 is the cheap poll: live panels only, no stats queries.
func TestMeDashboardWithoutStats(t *testing.T) {
	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := newMyTeamsServer(f, dir)
	a := f.AddUser(t, "agent", "a@example.com")
	f.AddTeam(t, "A", map[uuid.UUID]bool{a: false})
	mine := insertTeamCall(t, f, dir, a, "A")
	tok := tokenFor(t, s, f, a, "a@example.com", "agent")

	rec := doReq(t, s.Router(), "GET", "/v1/me/dashboard?stats=0", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats=0: %d %s", rec.Code, rec.Body)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["stats"]; ok {
		t.Fatalf("stats=0 still sent stats: %s", rec.Body)
	}
	var snap dashboardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if got := snapCallIDs(snap); !sameIDs(got, []uuid.UUID{mine}) || len(snap.Queues.Queues) != 1 {
		t.Fatalf("stats=0 snapshot = %+v", snap)
	}
	if full := getMeDashboard(t, s, tok); len(full.Stats.Hourly) != 12 {
		t.Fatalf("default still includes stats: %+v", full.Stats)
	}
}

// The dashboard stream authenticates once at connect, so it must re-check on
// its keepalive: revoking the user's sessions (a password reset bumps
// session_epoch; so does deleting or demoting them) ends an open stream instead
// of leaving it to deliver every tenant event indefinitely.
func TestDashboardStreamEndsWhenSessionRevoked(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	s.streamKeepalive = 50 * time.Millisecond
	sup := f.AddUser(t, "supervisor", "s@example.com")
	tok := tokenFor(t, s, f, sup, "s@example.com", "supervisor")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "/v1/stream/dashboard", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.Router().ServeHTTP(rec, r)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("stream ended while the session was still valid")
	case <-time.After(300 * time.Millisecond): // several keepalives pass
	}

	if _, err := f.DB.Pool.Exec(context.Background(),
		`UPDATE users SET session_epoch = session_epoch + 1 WHERE id = $1`, sup); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream kept running after the session was revoked")
	}
}
