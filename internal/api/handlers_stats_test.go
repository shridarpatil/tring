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

func ptr[T any](v T) *T { return &v }
