package eslcdr

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func TestJobFailure(t *testing.T) {
	cases := []struct {
		reply, cause string
		failed       bool
	}{
		{"-ERR NO_ROUTE_DESTINATION\n", "NO_ROUTE_DESTINATION", true},
		{"-ERR GATEWAY_DOWN", "GATEWAY_DOWN", true},
		{"+OK 7a873c14-4fac-4957-97cd-567916b72d8b\n", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		cause, failed := jobFailure(c.reply)
		if cause != c.cause || failed != c.failed {
			t.Errorf("jobFailure(%q) = (%q, %v), want (%q, %v)", c.reply, cause, failed, c.cause, c.failed)
		}
	}
}

func TestSettlement(t *testing.T) {
	start := time.Unix(1000, 0)
	answered := start.Add(10 * time.Second)
	now := start.Add(70 * time.Second)
	if st, d := settlement(start, nil, now); st != "failed" || d != 70 {
		t.Errorf("unanswered: got (%s, %d), want (failed, 70)", st, d)
	}
	if st, d := settlement(start, &answered, now); st != "ended" || d != 60 {
		t.Errorf("answered: got (%s, %d), want (ended, 60)", st, d)
	}
}

func TestAnswerTime(t *testing.T) {
	for _, in := range []string{"", "0", "junk"} {
		if got := answerTime(in); got != nil {
			t.Errorf("answerTime(%q) = %v, want nil", in, got)
		}
	}
	if got := answerTime("1700000000123456"); got == nil || !got.Equal(time.UnixMicro(1700000000123456)) {
		t.Errorf("answerTime = %v", got)
	}
}

// TestHandle drives the event → call row transitions against a real Postgres.
func TestHandle(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	w := New(f.DB, "", "", slog.Default())

	seed := func(leg, job string) {
		t.Helper()
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, job_uuid)
			VALUES ($1, 'outbound', 'ringing', '+919800000001', '+919800000002', $2, $3)`, f.TenantID, leg, job); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	row := func(leg string) (status, cause string, answered bool) {
		t.Helper()
		var c *string
		var a *time.Time
		if err := f.DB.Pool.QueryRow(ctx, `SELECT status::text, hangup_cause, answered_at FROM calls WHERE leg_id = $1`, leg).
			Scan(&status, &c, &a); err != nil {
			t.Fatalf("read row: %v", err)
		}
		if c != nil {
			cause = *c
		}
		return status, cause, a != nil
	}

	t.Run("originate fails before a channel exists", func(t *testing.T) {
		seed("leg-job", "job-1")
		w.handle(ctx, event{name: "BACKGROUND_JOB", jobUUID: "job-1", jobReply: "-ERR GATEWAY_DOWN\n"})
		if st, cause, _ := row("leg-job"); st != "failed" || cause != "GATEWAY_DOWN" {
			t.Errorf("got (%s, %q), want (failed, GATEWAY_DOWN)", st, cause)
		}
	})

	t.Run("successful job leaves the row ringing", func(t *testing.T) {
		seed("leg-ok", "job-2")
		w.handle(ctx, event{name: "BACKGROUND_JOB", jobUUID: "job-2", jobReply: "+OK leg-ok\n"})
		if st, _, _ := row("leg-ok"); st != "ringing" {
			t.Errorf("got %s, want ringing", st)
		}
	})

	t.Run("unanswered hangup fails with the cause", func(t *testing.T) {
		seed("leg-noans", "job-3")
		w.handle(ctx, event{name: "CHANNEL_HANGUP_COMPLETE", tracked: true, legID: "leg-noans", cause: "NO_ANSWER", answered: "0"})
		if st, cause, _ := row("leg-noans"); st != "failed" || cause != "NO_ANSWER" {
			t.Errorf("got (%s, %q), want (failed, NO_ANSWER)", st, cause)
		}
	})

	t.Run("answer then hangup ends the call", func(t *testing.T) {
		seed("leg-ans", "job-4")
		w.handle(ctx, event{name: "CHANNEL_ANSWER", tracked: true, legID: "leg-ans"})
		if st, _, answered := row("leg-ans"); st != "answered" || !answered {
			t.Errorf("after answer: got (%s, answered=%v)", st, answered)
		}
		w.handle(ctx, event{name: "CHANNEL_HANGUP_COMPLETE", tracked: true, legID: "leg-ans", cause: "NORMAL_CLEARING"})
		if st, cause, _ := row("leg-ans"); st != "ended" || cause != "NORMAL_CLEARING" {
			t.Errorf("after hangup: got (%s, %q), want (ended, NORMAL_CLEARING)", st, cause)
		}
	})

	t.Run("hangup handled before its answer still ends the call", func(t *testing.T) {
		seed("leg-race", "job-5")
		at := strconv.FormatInt(time.Now().Add(-3*time.Second).UnixMicro(), 10)
		w.handle(ctx, event{name: "CHANNEL_HANGUP_COMPLETE", tracked: true, legID: "leg-race", cause: "NORMAL_CLEARING", answered: at})
		w.handle(ctx, event{name: "CHANNEL_ANSWER", tracked: true, legID: "leg-race"})
		if st, _, _ := row("leg-race"); st != "ended" {
			t.Errorf("got %s, want ended", st)
		}
	})

	t.Run("events for unknown channels are ignored", func(t *testing.T) {
		w.handle(ctx, event{name: "CHANNEL_ANSWER", tracked: true, legID: "not-ours"})
		w.handle(ctx, event{name: "CHANNEL_HANGUP_COMPLETE", tracked: true, legID: "not-ours", cause: "NORMAL_CLEARING"})
		w.handle(ctx, event{name: "BACKGROUND_JOB", jobUUID: "not-ours", jobReply: "-ERR X"})
	})

	// A channel without TrackVar (a gateway plug leg, a bridge B-leg) is skipped
	// before the DB, even if its uuid happened to match a row.
	t.Run("untracked channel events are skipped", func(t *testing.T) {
		seed("leg-untracked", "job-6")
		w.handle(ctx, event{name: "CHANNEL_ANSWER", legID: "leg-untracked"})
		w.handle(ctx, event{name: "CHANNEL_HANGUP_COMPLETE", legID: "leg-untracked", cause: "NORMAL_CLEARING"})
		if st, _, answered := row("leg-untracked"); st != "ringing" || answered {
			t.Errorf("got (%s, answered=%v), want untouched ringing", st, answered)
		}
	})
}
