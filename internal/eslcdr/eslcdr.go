// Package eslcdr keeps the call rows of FreeSWITCH-originated calls (the API's
// POST /v1/calls) in step with what FreeSWITCH reports. Those calls are placed
// with bgapi, so the API only ever learns "queued"; the
// outcome arrives later as ESL events:
//
//	BACKGROUND_JOB -ERR     -> failed (no channel was ever created)
//	CHANNEL_ANSWER          -> answered
//	CHANNEL_HANGUP_COMPLETE -> ended, or failed if it was never answered
//
// Rows are matched by leg_id (the origination uuid) or job_uuid. The api tags
// the channels it originates with TrackVar; channel events without it — the
// gateway's plug legs, the B-legs of a bridge — are skipped before any DB
// lookup, so an inbound call costs no queries here.
package eslcdr

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/queries"
)

// Watcher holds one ESL event connection and applies call events to the DB.
type Watcher struct {
	db       *db.DB
	q        *queries.Queries
	addr     string
	password string
	log      *slog.Logger
}

func New(d *db.DB, addr, password string, logger *slog.Logger) *Watcher {
	return &Watcher{db: d, q: queries.MustLoad(), addr: addr, password: password, log: logger.With("component", "eslcdr")}
}

// Run keeps the ESL event connection up, reconnecting with capped exponential
// backoff (1s → 30s; reset after a session that stayed up ≥5s). Returns when
// ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	const (
		initialBackoff = time.Second
		maxBackoff     = 30 * time.Second
		healthyAfter   = 5 * time.Second
	)
	backoff := initialBackoff
	for {
		startedAt := time.Now()
		err := w.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(startedAt) >= healthyAfter {
			backoff = initialBackoff
		}
		w.log.Warn("esl event connection lost, reconnecting", "err", err, "reconnect_in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// TrackVar is the channel variable the api sets on the calls it originates
// (POST /v1/calls). Only channels carrying it can have a row to update.
const TrackVar = "tring_api_call"

func (w *Watcher) runOnce(ctx context.Context) error {
	disconnected := make(chan struct{})
	conn, err := eslgo.Dial(w.addr, w.password, func() { close(disconnected) })
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.RegisterEventListener(eslgo.EventListenAll, func(e *eslgo.Event) {
		// Own context: a DB write mustn't be cut short by shutdown mid-update.
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.handle(dbCtx, toEvent(e))
	})
	if _, err := conn.SendCommand(ctx, command.Event{
		Format: "plain",
		Listen: []string{"BACKGROUND_JOB", "CHANNEL_ANSWER", "CHANNEL_HANGUP_COMPLETE"},
	}); err != nil {
		return err
	}
	w.log.Info("watching freeswitch call events", "addr", w.addr)

	select {
	case <-ctx.Done():
		return nil
	case <-disconnected:
		return errors.New("esl disconnected")
	}
}

// toEvent reads the headers the watcher uses from an ESL event.
func toEvent(e *eslgo.Event) event {
	return event{
		name:     e.GetName(),
		legID:    e.GetHeader("Unique-ID"),
		tracked:  e.GetHeader("variable_"+TrackVar) != "",
		jobUUID:  e.GetHeader("Job-UUID"),
		cause:    e.GetHeader("Hangup-Cause"),
		answered: e.GetHeader("Caller-Channel-Answered-Time"),
		jobReply: string(e.Body),
	}
}

// event is the part of an ESL event the watcher reads.
type event struct {
	name     string
	legID    string // Unique-ID: the channel uuid (== calls.leg_id for the A-leg)
	tracked  bool   // the channel carries TrackVar (an api-originated call)
	jobUUID  string // BACKGROUND_JOB only
	cause    string // CHANNEL_HANGUP_COMPLETE only
	answered string // CHANNEL_HANGUP_COMPLETE: answer time in epoch µs, "0" if never
	jobReply string // BACKGROUND_JOB body: "+OK <uuid>" or "-ERR <cause>"
}

func (w *Watcher) handle(ctx context.Context, e event) {
	switch e.name {
	case "BACKGROUND_JOB":
		w.onJob(ctx, e)
	case "CHANNEL_ANSWER":
		w.onAnswer(ctx, e)
	case "CHANNEL_HANGUP_COMPLETE":
		w.onHangup(ctx, e)
	}
}

// onJob fails the row of an originate that never created a channel — no
// channel means no CHANNEL_HANGUP_COMPLETE, so this is the only signal.
func (w *Watcher) onJob(ctx context.Context, e event) {
	cause, failed := jobFailure(e.jobReply)
	if !failed || e.jobUUID == "" {
		return // success: the channel's own events settle the row
	}
	var callID uuid.UUID
	err := w.db.Pool.QueryRow(ctx, w.q.FailCallByJob, cause, e.jobUUID).Scan(&callID)
	if errors.Is(err, pgx.ErrNoRows) {
		return // not ours, or already settled
	}
	if err != nil {
		w.log.Error("mark call failed", "job_uuid", e.jobUUID, "err", err)
		return
	}
	w.log.Warn("call failed to start", "call", callID, "cause", cause)
}

func (w *Watcher) onAnswer(ctx context.Context, e event) {
	if e.legID == "" || !e.tracked {
		return // not a call the api originated
	}
	var callID uuid.UUID
	err := w.db.Pool.QueryRow(ctx, w.q.MarkCallAnswered, e.legID).Scan(&callID)
	if errors.Is(err, pgx.ErrNoRows) {
		return // not ours, or no longer ringing
	}
	if err != nil {
		w.log.Error("mark call answered", "leg", e.legID, "err", err)
		return
	}
	w.log.Info("call answered", "call", callID)
}

func (w *Watcher) onHangup(ctx context.Context, e event) {
	if e.legID == "" || !e.tracked {
		return // not a call the api originated
	}
	var (
		callID     uuid.UUID
		startedAt  time.Time
		answeredAt *time.Time
		status     string
	)
	if err := w.db.Pool.QueryRow(ctx, w.q.GetCallSettlement, e.legID).
		Scan(&callID, &startedAt, &answeredAt, &status); err != nil {
		return // not ours
	}
	if status == "ended" || status == "failed" {
		return
	}
	// eslgo runs each listener in its own goroutine, so this hangup can be
	// handled before its CHANNEL_ANSWER; the hangup's own answer time covers it.
	if answeredAt == nil {
		answeredAt = answerTime(e.answered)
	}
	final, dur := settlement(startedAt, answeredAt, time.Now())
	var cause any
	if e.cause != "" {
		cause = e.cause
	}
	if _, err := w.db.Pool.Exec(ctx, w.q.SettleCall, final, dur, cause, e.legID); err != nil {
		w.log.Error("settle call", "call", callID, "err", err)
		return
	}
	level := slog.LevelInfo
	if final == "failed" {
		level = slog.LevelWarn
	}
	w.log.Log(ctx, level, "call "+final, "call", callID, "cause", e.cause, "duration_s", dur)
}

// jobFailure reports whether a BACKGROUND_JOB reply is an originate failure
// ("-ERR NO_ROUTE_DESTINATION") and its cause.
func jobFailure(reply string) (cause string, failed bool) {
	reply = strings.TrimSpace(reply)
	if !strings.HasPrefix(reply, "-ERR") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(reply, "-ERR")), true
}

// answerTime parses a Caller-Channel-Answered-Time header (epoch µs; "0" or
// empty when the channel was never answered).
func answerTime(usec string) *time.Time {
	n, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	t := time.UnixMicro(n)
	return &t
}

// settlement is a hung-up call's final status and duration: ended and timed
// from answer if it was answered, otherwise failed and timed from start.
func settlement(startedAt time.Time, answeredAt *time.Time, now time.Time) (status string, seconds int) {
	if answeredAt == nil {
		return "failed", int(now.Sub(startedAt).Seconds())
	}
	return "ended", int(now.Sub(*answeredAt).Seconds())
}
