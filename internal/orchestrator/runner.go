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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/percipia/eslgo"
	"github.com/percipia/eslgo/command"

	"github.com/whatomate/tring/internal/blobstore"
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
	// recordingsDir is the FreeSWITCH-side directory recordings are written to
	// (shared mount the API also reads). Empty disables recording.
	recordingsDir string
	// blob is the optional S3 store recordings are uploaded to on completion.
	// nil/disabled → recordings stay on the local volume.
	blob *blobstore.Store
	log  *slog.Logger
}

// WithRecordings sets the directory FreeSWITCH writes call recordings to and
// returns the runner for chaining. Empty leaves recording off.
func (r *Runner) WithRecordings(dir string) *Runner {
	r.recordingsDir = dir
	return r
}

// WithBlobStore attaches the S3 store recordings are uploaded to on RECORD_STOP
// and returns the runner for chaining. A nil/disabled store keeps them local.
func (r *Runner) WithBlobStore(b *blobstore.Store) *Runner {
	r.blob = b
	return r
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
		r.notifier.WithLogger(l)
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
	case "RECORD_STOP":
		r.onRecordStop(e)
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

	// Agent and client are now connected — start recording if the tenant has it
	// enabled. Done before agent attribution so it covers BOTH inbound transfers
	// (agent is the tagged B-leg) and outbound click-to-call (agent is the A-leg,
	// client the B-leg) — either way we record the A-leg, which carries the
	// bridged audio. RECORD_STEREO splits the two parties onto separate channels;
	// uuid_record stops itself on hangup, so there's no explicit stop.
	r.maybeRecord(ctx, conn, aLeg, tenantID)

	sip, _ := getChannelVar(ctx, conn, bLeg, "tring_agent")
	if sip == "" {
		// No agent tag on the B-leg → this is the customer leg of an outbound
		// click-to-call (the agent is the A-leg). Record it so hold plays MOH to
		// the customer. Inbound is unaffected: there the customer is the A-leg
		// (= fs_uuid), which control resolves via COALESCE.
		if _, err := r.db.Pool.Exec(ctx, r.q.SetCallCustomerLeg, bLeg, aLeg); err != nil {
			r.log.Error("stamp customer leg failed", "call_id", aLeg, "err", err)
		}
		return
	}
	var (
		agentID uuid.UUID
		label   string
	)
	if err := r.db.Pool.QueryRow(ctx, r.q.GetAgentBySIP, tenantID, sip).Scan(&agentID, &label); err != nil {
		r.log.Error("bridge agent lookup failed", "call_id", aLeg, "sip", sip, "err", err)
		return
	}
	// Store the agent's leg (bLeg) too so call control (hold/transfer) can act on
	// the right side — holding the agent leg makes the customer hear MOH.
	if _, err := r.db.Pool.Exec(ctx, r.q.SetCallAgent, agentID, aLeg, bLeg); err != nil {
		r.log.Error("stamp agent on call failed", "call_id", aLeg, "err", err)
		return
	}
	r.log.Info("call answered by agent", "call_id", aLeg, "sip", sip, "label", label)
}

// maybeRecord starts a WAV recording of the connected call when the tenant's
// record_calls flag is on. Best-effort: a failure is logged and the call
// continues (recording must never break a live call).
func (r *Runner) maybeRecord(ctx context.Context, conn *eslgo.Conn, aLeg string, tenantID uuid.UUID) {
	if r.recordingsDir == "" {
		return
	}
	var enabled bool
	if err := r.db.Pool.QueryRow(ctx, r.q.GetTenantRecordCalls, tenantID).Scan(&enabled); err != nil || !enabled {
		return
	}
	path := filepath.Join(r.recordingsDir, aLeg+".wav")
	_, _ = conn.SendCommand(ctx, command.API{Command: "uuid_setvar", Arguments: aLeg + " RECORD_STEREO true"})
	resp, err := conn.SendCommand(ctx, command.API{Command: "uuid_record", Arguments: aLeg + " start " + path})
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(resp.Body)), "+OK") {
		r.log.Warn("call recording failed to start", "call_id", aLeg, "path", path, "err", err)
		return
	}
	if _, err := r.db.Pool.Exec(ctx, r.q.SetCallRecording, aLeg); err != nil {
		r.log.Error("set recording_url failed", "call_id", aLeg, "err", err)
		return
	}
	r.log.Info("call recording started", "call_id", aLeg, "path", path)
}

// onRecordStop uploads a finished recording to S3 (date-wise key) and deletes
// the local spool, making S3 the source of truth. RECORD_STOP fires exactly
// when FreeSWITCH finalizes the WAV and carries its path. No-op when S3 is off
// (the file then stays on the local volume for the API to serve directly).
// Best-effort: on any failure the local file is left in place and the API can
// still serve it. Uses its own longer context — an upload can outlast the 5s
// event context.
func (r *Runner) onRecordStop(e *eslgo.Event) {
	if !r.blob.Enabled() {
		return
	}
	fsUUID := e.GetHeader("Unique-ID")
	recPath := e.GetHeader("Record-File-Path")
	if fsUUID == "" || recPath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var (
		tenantID  uuid.UUID
		startedAt time.Time
	)
	if err := r.db.Pool.QueryRow(ctx, r.q.GetCallForRecording, fsUUID).Scan(&tenantID, &startedAt); err != nil {
		return // not a Tring-tracked call
	}
	key := r.blob.Key(tenantID.String(), startedAt, fsUUID+".wav")
	if err := r.blob.PutFile(ctx, key, recPath, "audio/wav"); err != nil {
		r.log.Error("recording S3 upload failed", "call_id", fsUUID, "key", key, "err", err)
		return
	}
	if _, err := r.db.Pool.Exec(ctx, r.q.SetCallRecordingS3, fsUUID, key); err != nil {
		r.log.Error("set recording_s3_key failed", "call_id", fsUUID, "err", err)
		return
	}
	_ = os.Remove(recPath) // S3 is the source of truth now; drop the local spool
	r.log.Info("recording uploaded to S3", "call_id", fsUUID, "key", key)
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
