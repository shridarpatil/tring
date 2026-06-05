// Package orchestrator runs the long-lived FreeSWITCH-facing process. It
// holds a persistent ESL connection, subscribes to channel events, and
// keeps the `calls` table in sync with FreeSWITCH reality:
//
//	CHANNEL_ANSWER          -> calls.status=answered, answered_at=now()
//	CHANNEL_HANGUP_COMPLETE -> calls.status=ended,    ended_at, duration, hangup_cause
//
// The API service triggers originates over short-lived ESL connections;
// the orchestrator does NOT issue originate commands. Keeping these roles
// separate matters because the orchestrator must be stable (one socket,
// one event loop), while the API is stateless and horizontally scalable.
package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/percipia/eslgo"

	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/queries"
	"github.com/whatomate/tring/internal/webhook"
)

type Runner struct {
	db          *db.DB
	q           *queries.Queries
	notifier    *webhook.Notifier
	eslAddr     string
	eslPassword string
	log         *slog.Logger
}

func New(d *db.DB, addr, password string) *Runner {
	return &Runner{
		db:          d,
		q:           queries.MustLoad(),
		notifier:    webhook.New(d),
		eslAddr:     addr,
		eslPassword: password,
		log:         slog.Default(),
	}
}

// WithLogger sets the runner's logger (scoped component=orchestrator) and
// returns it for chaining. Event-loop lines that pertain to a specific call
// bind call_id so they correlate with the IVR executor's per-call lines.
func (r *Runner) WithLogger(l *slog.Logger) *Runner {
	if l != nil {
		r.log = l.With("component", "orchestrator")
	}
	return r
}

// Run keeps a connection to FreeSWITCH ESL alive. On any disconnect it
// reconnects with capped exponential backoff (1s → 2 → 4 → 8 → 16 → 30s).
// A session that ran healthy for ≥5s resets the backoff back to 1s on the
// next failure, so a one-off FS restart doesn't poison subsequent reconnect
// timings into 30s waits. Returns only when ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 30 * time.Second
		healthyAfter   = 5 * time.Second
	)
	backoff := initialBackoff
	for {
		startedAt := time.Now()
		err := r.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		sessionDuration := time.Since(startedAt)
		if sessionDuration >= healthyAfter {
			backoff = initialBackoff
		}
		r.log.Warn("esl session ended, reconnecting",
			"after", sessionDuration.Round(time.Millisecond).String(), "err", err, "reconnect_in", backoff.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) error {
	disconnected := make(chan struct{})
	conn, err := eslgo.Dial(r.eslAddr, r.eslPassword, func() { close(disconnected) })
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.EnableEvents(ctx); err != nil {
		return err
	}
	r.log.Info("connected to esl, subscribed to events", "addr", r.eslAddr)

	conn.RegisterEventListener(eslgo.EventListenAll, func(e *eslgo.Event) {
		// Use background ctx for DB writes so a slow query doesn't get
		// cancelled mid-flight if the parent ctx flips. Bound separately.
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.handleEvent(dbCtx, conn, e)
	})

	select {
	case <-ctx.Done():
		return nil
	case <-disconnected:
		return errors.New("esl disconnected")
	}
}

func (r *Runner) handleEvent(ctx context.Context, conn *eslgo.Conn, e *eslgo.Event) {
	switch e.GetName() {
	case "CHANNEL_ANSWER":
		r.onAnswer(ctx, e)
	case "CHANNEL_HANGUP_COMPLETE":
		r.onHangup(ctx, e)
	case "CHANNEL_BRIDGE":
		r.onBridge(ctx, conn, e)
	case "BACKGROUND_JOB":
		r.onBackgroundJob(ctx, e)
	}
}

// onBridge attributes an answered IVR transfer to the specific agent who
// picked up. CHANNEL_BRIDGE fires exactly when two legs connect and carries
// both leg UUIDs; the answering agent leg was tagged with a `tring_agent`
// channel variable when the transfer dialed it (see agentLeg). We read that
// off the B-leg (alive at bridge time) and stamp calls.agent_id on the A-leg's
// call row. This is the reliable signal — far better than probing post-bridge
// channel variables, which race with whichever leg hangs up first.
func (r *Runner) onBridge(ctx context.Context, conn *eslgo.Conn, e *eslgo.Event) {
	aLeg := e.GetHeader("Bridge-A-Unique-ID")
	bLeg := e.GetHeader("Bridge-B-Unique-ID")
	if aLeg == "" || bLeg == "" {
		return
	}
	// Scope to a Tring call; ErrNoRows means this bridge isn't ours.
	var tenantID uuid.UUID
	if err := r.db.Pool.QueryRow(ctx, r.q.GetCallTenant, aLeg).Scan(&tenantID); err != nil {
		return
	}
	sip, _ := getChannelVar(ctx, conn, bLeg, "tring_agent")
	if sip == "" {
		return // bridged leg isn't a tagged agent leg (e.g. not a transfer)
	}
	var (
		agentID uuid.UUID
		label   string
	)
	if err := r.db.Pool.QueryRow(ctx, r.q.GetAgentBySIP, tenantID, sip).Scan(&agentID, &label); err != nil {
		r.log.Error("bridge agent lookup failed", "call_id", aLeg, "sip", sip, "err", err)
		return
	}
	if _, err := r.db.Pool.Exec(ctx, r.q.SetCallAgent, agentID, aLeg); err != nil {
		r.log.Error("stamp agent on call failed", "call_id", aLeg, "err", err)
		return
	}
	r.log.Info("call answered by agent", "call_id", aLeg, "sip", sip, "label", label)
}

// onBackgroundJob settles call rows whose bgapi originate failed before any
// channel was created. FS only emits CHANNEL_HANGUP_COMPLETE for channels
// that actually existed; for a fully-failed originate (e.g., user not
// registered), the only event we ever get is BACKGROUND_JOB carrying the
// "-ERR <cause>" body.
func (r *Runner) onBackgroundJob(ctx context.Context, e *eslgo.Event) {
	jobUUID := e.GetHeader("Job-UUID")
	if jobUUID == "" {
		return
	}
	body := strings.TrimSpace(string(e.Body))
	if !strings.HasPrefix(body, "-ERR") {
		// Success or non-originate job — the CHANNEL_HANGUP_COMPLETE path
		// will handle settlement once the channel ends.
		return
	}
	cause := strings.TrimSpace(strings.TrimPrefix(body, "-ERR"))
	var fsUUID string
	err := r.db.Pool.QueryRow(ctx, r.q.FailCallByJob, cause, jobUUID).Scan(&fsUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return // not a Tring originate, or already settled
	}
	if err != nil {
		r.log.Error("bg job update failed", "job_uuid", jobUUID, "err", err)
		return
	}
	r.log.Info("originate failed (no channel created)", "job_uuid", jobUUID, "cause", cause)
	if tenantID, payload, ok := r.loadCallPayload(ctx, fsUUID); ok {
		r.notifier.Emit(ctx, tenantID, webhook.EventCallEnded, payload)
	}
}

func (r *Runner) onAnswer(ctx context.Context, e *eslgo.Event) {
	fsUUID := e.GetHeader("Unique-ID")
	if fsUUID == "" {
		return
	}
	// Only flip rows that are still ringing — protects against out-of-order
	// or replayed events.
	tag, err := r.db.Pool.Exec(ctx, r.q.MarkCallAnswered, fsUUID)
	if err != nil {
		r.log.Error("answer update failed", "call_id", fsUUID, "err", err)
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	r.log.Info("call answered", "call_id", fsUUID)
	if tenantID, payload, ok := r.loadCallPayload(ctx, fsUUID); ok {
		r.notifier.Emit(ctx, tenantID, webhook.EventCallAnswered, payload)
	}
}

func (r *Runner) onHangup(ctx context.Context, e *eslgo.Event) {
	uuid := e.GetHeader("Unique-ID")
	if uuid == "" {
		return
	}
	cause := e.GetHeader("Hangup-Cause")

	// Look up the current row to decide final status and compute duration.
	var (
		startedAt  time.Time
		answeredAt *time.Time
		status     string
	)
	err := r.db.Pool.QueryRow(ctx, r.q.GetCallSettlement, uuid).Scan(&startedAt, &answeredAt, &status)
	if err != nil {
		// Not a Tring-tracked call (e.g., registration probe). Ignore quietly.
		return
	}
	if status == "ended" || status == "failed" {
		return // already settled
	}

	finalStatus := "ended"
	if answeredAt == nil {
		finalStatus = "failed"
	}

	now := time.Now()
	var dur int
	if answeredAt != nil {
		dur = int(now.Sub(*answeredAt).Seconds())
	} else {
		dur = int(now.Sub(startedAt).Seconds())
	}

	_, err = r.db.Pool.Exec(ctx, r.q.SettleCall, finalStatus, dur, nullIfEmpty(cause), uuid)
	if err != nil {
		r.log.Error("hangup update failed", "call_id", uuid, "err", err)
		return
	}
	r.log.Info("call ended", "call_id", uuid, "status", finalStatus, "cause", cause, "duration_s", dur)
	if tenantID, payload, ok := r.loadCallPayload(ctx, uuid); ok {
		r.notifier.Emit(ctx, tenantID, webhook.EventCallEnded, payload)
	}
}

// loadCallPayload fetches the post-update call row as a webhook-friendly
// map (matching the REST GET shape) and returns the tenant_id so the
// notifier can scope its lookup.
func (r *Runner) loadCallPayload(ctx context.Context, fsUUID string) (uuid.UUID, map[string]interface{}, bool) {
	var (
		id, tenantID                                           uuid.UUID
		direction, status, fromNumber, toNumber, jobUUID, fsID string
		hangupCause                                            *string
		recordingURL                                           *string
		agentID                                                *uuid.UUID
		startedAt                                              time.Time
		answeredAt, endedAt                                    *time.Time
		duration                                               *int
	)
	err := r.db.Pool.QueryRow(ctx, r.q.GetCallPayload, fsUUID).Scan(&id, &tenantID, &direction, &status,
		&fromNumber, &toNumber, &agentID,
		&fsID, &jobUUID,
		&startedAt, &answeredAt, &endedAt,
		&duration, &hangupCause, &recordingURL)
	if err != nil {
		return uuid.Nil, nil, false
	}
	return tenantID, map[string]interface{}{
		"call": map[string]interface{}{
			"id":               id,
			"tenant_id":        tenantID,
			"direction":        direction,
			"status":           status,
			"from_number":      fromNumber,
			"to_number":        toNumber,
			"agent_id":         agentID,
			"fs_uuid":          fsID,
			"started_at":       startedAt,
			"answered_at":      answeredAt,
			"ended_at":         endedAt,
			"duration_seconds": duration,
			"hangup_cause":     hangupCause,
			"recording_url":    recordingURL,
		},
	}, true
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
