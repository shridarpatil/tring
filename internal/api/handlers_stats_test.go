package api

import (
	"context"
	"math"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// Avg talk counts only answered time: an IVR-only call adds nothing, an
// answered inbound call counts from answer to end, and an outbound call (whose
// row exists only once the callee picks up) counts from start.
func TestStatsTodayAvgTalk(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	s := newTestServer(f)

	// Offsets from today's midnight, so every row is "today" whenever the test runs.
	seed := `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id,
		started_at, answered_at, ended_at, duration_seconds)
		VALUES ($1, $2, 'ended', '+919800000001', '+919800000089', $3,
		date_trunc('day', now()) + make_interval(secs => $4),
		date_trunc('day', now()) + make_interval(secs => $5::float8),
		date_trunc('day', now()) + make_interval(secs => $6), $7)`
	for _, c := range []struct {
		dir, leg string
		start    float64
		answered *float64
		end      float64
		duration int
	}{
		{"inbound", "ivr-only", 60, nil, 70, 10},          // IVR, nobody answered
		{"inbound", "answered", 100, ptr(112.0), 160, 60}, // 12 s IVR + ring, 48 s talk
		{"outbound", "outbound", 200, nil, 230, 30},       // 30 s talk
	} {
		if _, err := f.DB.Pool.Exec(ctx, seed, f.TenantID, c.dir, c.leg, c.start, c.answered, c.end, c.duration); err != nil {
			t.Fatalf("seed %s: %v", c.leg, err)
		}
	}

	var total, answered, abandoned, failed int
	var avgWait, avgTalk float64
	if err := f.DB.Pool.QueryRow(ctx, s.q.StatsToday, f.TenantID).Scan(&total, &answered, &abandoned, &failed, &avgWait, &avgTalk); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if want := 39.0; math.Abs(avgTalk-want) > 0.01 { // (48 + 30) / 2
		t.Fatalf("avg talk = %.2f s, want %.0f (IVR-only time must not count)", avgTalk, want)
	}
}

// Breaches count every stint that outlasted its snapshot SLA whatever the
// outcome (open ones measured to now); SLA 0 never breaches; yesterday is out.
// Avg queue wait covers answered stints only.
func TestStatsQueueBreaches(t *testing.T) {
	f := dbtest.Setup(t)
	skipNearMidnight(t, f)
	ctx := context.Background()
	s := newTestServer(f)

	seed := `INSERT INTO queue_stints (tenant_id, team, sla_seconds, enqueued_at, ended_at, outcome)
		VALUES ($1, $2, $3, now() - make_interval(secs => $4::float8),
		        CASE WHEN $5::float8 IS NULL THEN NULL ELSE now() - make_interval(secs => $4::float8) + make_interval(secs => $5::float8) END, $6)`
	day := 86400.0
	for _, c := range []struct {
		name    string
		sla     int
		ago     float64
		waited  *float64
		outcome *string
	}{
		{"answered-late", 300, 500, ptr(400.0), ptr("answered")},
		{"abandoned-late", 300, 500, ptr(360.0), ptr("abandoned")},
		{"answered-fast", 300, 200, ptr(60.0), ptr("answered")},
		{"open-late", 300, 310, nil, nil},
		{"no-sla", 0, 1000, ptr(900.0), ptr("abandoned")},
		{"yesterday", 300, day, ptr(400.0), ptr("answered")},
	} {
		if _, err := f.DB.Pool.Exec(ctx, seed, f.TenantID, "sales", c.sla, c.ago, c.waited, c.outcome); err != nil {
			t.Fatalf("seed %s: %v", c.name, err)
		}
	}

	out, err := s.statsOverview(ctx, f.TenantID)
	if err != nil {
		t.Fatalf("statsOverview: %v", err)
	}
	if out.SLABreachesToday != 3 {
		t.Fatalf("sla_breaches_today = %d, want 3", out.SLABreachesToday)
	}
	if out.AvgQueueWaitSeconds != 230 { // (400 + 60) / 2
		t.Fatalf("avg_queue_wait_seconds = %d, want 230", out.AvgQueueWaitSeconds)
	}
	if len(out.Hourly) != 12 {
		t.Fatalf("hourly rows = %d, want 12", len(out.Hourly))
	}
	sum := 0
	for _, b := range out.Hourly {
		sum += b.Breaches
	}
	if sum != 3 { // summed: the stints may straddle an hour boundary
		t.Fatalf("hourly breaches = %d, want 3", sum)
	}
}

func ptr[T any](v T) *T { return &v }
