package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// queueFor returns the named team's row from a snapshot.
func queueFor(t *testing.T, out queuesResponse, team string) queueDepth {
	t.Helper()
	for _, q := range out.Queues {
		if q.Team == team {
			return q
		}
	}
	t.Fatalf("team %q missing from %+v", team, out.Queues)
	return queueDepth{}
}

// seedStint inserts a stint enqueued `ago` before now; ended stints end now.
func seedStint(t *testing.T, f *dbtest.Fixture, callID *uuid.UUID, team string, sla int, ago time.Duration, ended bool) {
	t.Helper()
	q := `INSERT INTO queue_stints (tenant_id, call_id, team, sla_seconds, enqueued_at, ended_at, outcome)
		VALUES ($1, $2, $3, $4, now() - make_interval(secs => $5),
		        CASE WHEN $6 THEN now() END, CASE WHEN $6 THEN 'answered' END)`
	if _, err := f.DB.Pool.Exec(context.Background(), q, f.TenantID, callID, team, sla, ago.Seconds(), ended); err != nil {
		t.Fatalf("seed stint: %v", err)
	}
}

func seedTeam(t *testing.T, f *dbtest.Fixture, name string, minLive, sla int, hours string) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(),
		`INSERT INTO teams (tenant_id, name, min_live_agents, sla_seconds, hours) VALUES ($1, $2, $3, $4, $5)`,
		f.TenantID, name, minLive, sla, hours); err != nil {
		t.Fatalf("seed team: %v", err)
	}
}

func TestQueuesSnapshotStints(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	seedTeam(t, f, "support", 0, 300, "[]")
	seedStint(t, f, nil, "support", 300, 400*time.Second, false) // waiting, past SLA
	seedStint(t, f, nil, "support", 300, 10*time.Second, false)  // waiting
	seedStint(t, f, nil, "support", 300, 30*time.Second, true)   // ended, within SLA
	seedStint(t, f, nil, "support", 300, 900*time.Second, true)  // ended past SLA today

	out, err := s.queuesSnapshot(context.Background(), &Tenant{ID: f.TenantID}, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	q := queueFor(t, out, "support")
	if len(q.WaitingSince) != 2 {
		t.Fatalf("waiting_since = %v, want 2 open stints", q.WaitingSince)
	}
	if !q.WaitingSince[0].Before(q.WaitingSince[1]) {
		t.Fatalf("waiting_since not oldest first: %v", q.WaitingSince)
	}
	if age := time.Since(q.WaitingSince[0]); age < 390*time.Second || age > 420*time.Second {
		t.Fatalf("oldest wait age = %v, want ~400s", age)
	}
	if q.SLASeconds != 300 {
		t.Fatalf("sla_seconds = %d, want 300", q.SLASeconds)
	}
	if q.BreachesToday != 2 {
		t.Fatalf("breaches_today = %d, want 2 (open past SLA + ended past SLA)", q.BreachesToday)
	}
}

func TestQueuesSnapshotIgnoresStaleStints(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	seedTeam(t, f, "support", 0, 0, "[]")
	seedStint(t, f, nil, "support", 0, 5*time.Hour, false) // orphaned by a crash

	out, err := s.queuesSnapshot(context.Background(), &Tenant{ID: f.TenantID}, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if q := queueFor(t, out, "support"); len(q.WaitingSince) != 0 {
		t.Fatalf("waiting_since = %v, want none (stale stint)", q.WaitingSince)
	}
}

func TestQueuesSnapshotClosedTeam(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	tomorrow := strings.ToLower(time.Now().AddDate(0, 0, 1).Weekday().String())
	hours := `[{"day":"` + tomorrow + `","enabled":true,"start_time":"00:00","end_time":"23:59"}]`
	seedTeam(t, f, "closed", 2, 0, hours)
	seedTeam(t, f, "open", 2, 0, "[]")

	out, err := s.queuesSnapshot(context.Background(), &Tenant{ID: f.TenantID}, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	c := queueFor(t, out, "closed")
	if c.Open {
		t.Fatal("closed team reads open")
	}
	if c.MinLive != 2 || c.Live != 0 {
		t.Fatalf("min/live = %d/%d, want 2/0", c.MinLive, c.Live)
	}
	if c.BelowMinSince != nil {
		t.Fatalf("closed team below_min_since = %v, want absent", c.BelowMinSince)
	}
	if o := queueFor(t, out, "open"); !o.Open || o.BelowMinSince == nil {
		t.Fatalf("open understaffed team = %+v, want open with below_min_since", o)
	}
}

func TestQueuesSnapshotUnconfigured(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO teams (tenant_id, name) VALUES ($1, 'support')`, f.TenantID); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	out, err := s.queuesSnapshot(context.Background(), &Tenant{ID: f.TenantID}, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	q := queueFor(t, out, "support")
	if !q.Open || q.MinLive != 0 || q.SLASeconds != 0 || q.BelowMinSince != nil {
		t.Fatalf("unconfigured team = %+v, want open, no minimum, no SLA, no below_min_since", q)
	}
	raw, _ := json.Marshal(q)
	if !strings.Contains(string(raw), `"waiting_since":[]`) {
		t.Fatalf("waiting_since must be [] not null: %s", raw)
	}
	if strings.Contains(string(raw), "below_min_since") {
		t.Fatalf("below_min_since must be omitted: %s", raw)
	}
}

func TestActiveCallsQueueFields(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)
	ctx := context.Background()
	newCall := func(leg string) uuid.UUID {
		var id uuid.UUID
		if err := f.DB.Pool.QueryRow(ctx, `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id)
			VALUES ($1, 'inbound', 'ringing', '+919800000001', '+919800000089', $2) RETURNING id`, f.TenantID, leg).Scan(&id); err != nil {
			t.Fatalf("seed call: %v", err)
		}
		return id
	}
	queued, plain := newCall("queued"), newCall("plain")
	seedStint(t, f, &queued, "support", 0, 10*time.Minute, true) // an earlier, ended stint
	seedStint(t, f, &queued, "billing", 120, 20*time.Second, false)

	calls, err := s.activeCallsList(ctx, f.TenantID)
	if err != nil {
		t.Fatalf("active calls: %v", err)
	}
	byID := map[uuid.UUID]Call{}
	for _, c := range calls {
		byID[c.ID] = c
	}
	q := byID[queued]
	if q.QueueTeam == nil || *q.QueueTeam != "billing" {
		t.Fatalf("queue_team = %v, want billing", q.QueueTeam)
	}
	if q.QueuedAt == nil || time.Since(*q.QueuedAt) > time.Minute {
		t.Fatalf("queued_at = %v, want ~20s ago", q.QueuedAt)
	}
	if q.QueueSLASeconds == nil || *q.QueueSLASeconds != 120 {
		t.Fatalf("queue_sla_seconds = %v, want 120", q.QueueSLASeconds)
	}
	raw, _ := json.Marshal(byID[plain])
	for _, k := range []string{"queue_team", "queued_at", "queue_sla_seconds"} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("unqueued call has %s: %s", k, raw)
		}
	}
}
