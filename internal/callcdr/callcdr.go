// Package callcdr records media-plane calls (web /rtc + the PSTN plug) into the
// SAME `calls` table, recording pipeline, and serve endpoint the FreeSWITCH path
// uses — one CDR + one recording story for every call source, per the
// convergence plan (docs/MEDIA_CONVERGENCE_PLAN.md).
//
// The existing machinery keys everything on `calls.fs_uuid` as an opaque per-call
// leg id and serves local recordings from <recordingsDir>/<fs_uuid>.<format>.
// Media calls reuse all of it with a generated leg id: the call row is created on
// call start (ringing), marked answered when an agent joins, settled on end, and
// — when the tenant has record_calls on — both legs are captured losslessly via
// a record.Recorder (room.TrackSink) and merged to ONE stereo Ogg at finalize:
// agent LEFT, customer RIGHT (never mono). S3 upload + recording_s3_key stamping
// mirror the orchestrator's date-wise key scheme.
package callcdr

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/blobstore"
	"github.com/whatomate/tring/internal/db"
	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/record"
	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/queries"
)

// Log is the media-plane call logger. One instance per gateway; safe for
// concurrent calls.
type Log struct {
	db   *db.DB
	q    *queries.Queries
	blob *blobstore.Store // nil/disabled → recordings stay on local disk
	dir  string           // local recordings spool (shared with the API's serve)
	log  *slog.Logger

	mu    sync.Mutex
	calls map[string]*activeCall // by room id
}

type activeCall struct {
	id        uuid.UUID // calls.id
	legID     string    // stored in calls.fs_uuid; names the recording file
	tenant    uuid.UUID
	startedAt time.Time
	callerPID room.ParticipantID
	rec       *record.Recorder // nil = tenant doesn't record
}

// New builds a Log. blob may be nil (S3 off); dir is the recordings spool.
func New(d *db.DB, blob *blobstore.Store, dir string, logger *slog.Logger) *Log {
	if logger == nil {
		logger = slog.Default()
	}
	// Probe the spool NOW and complain loudly: recorder write errors are dropped
	// on the media hot path (sinks are best-effort), so an unwritable spool —
	// classically a root-owned volume vs this non-root process — would otherwise
	// surface only as silent "no media captured".
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("callcdr: recordings spool unusable — recordings will be SKIPPED", "dir", dir, "err", err)
	} else if probe, err := os.CreateTemp(dir, ".probe-*"); err != nil {
		logger.Error("callcdr: recordings spool not writable — recordings will be SKIPPED "+
			"(volume owned by root? chmod 1777 it)", "dir", dir, "err", err)
	} else {
		probe.Close()
		_ = os.Remove(probe.Name())
	}
	return &Log{
		db:    d,
		q:     queries.MustLoad(),
		blob:  blob,
		dir:   dir,
		log:   logger.With("component", "callcdr"),
		calls: map[string]*activeCall{},
	}
}

// Started opens the CDR for a call (status ringing) and, when the tenant records
// calls, returns the recorder as a TrackSink to attach to the CALLER's
// publication (nil → not recording). callerPID is labeled "customer".
// Best-effort: a DB failure logs and returns nil — the call proceeds unrecorded
// rather than failing.
func (l *Log) Started(ctx context.Context, tenant uuid.UUID, roomID, from, to, clientRef string, callerPID room.ParticipantID) room.TrackSink {
	legID := uuid.NewString()
	var callID uuid.UUID
	err := l.db.Pool.QueryRow(ctx, l.q.CreateCallInbound, tenant, from, to, legID, clientRef).Scan(&callID)
	if err != nil {
		l.log.Error("cdr: create call failed", "room", roomID, "err", err)
		return nil
	}

	ac := &activeCall{id: callID, legID: legID, tenant: tenant, startedAt: time.Now(), callerPID: callerPID}
	var recordOn bool
	if err := l.db.Pool.QueryRow(ctx, l.q.GetTenantRecordCalls, tenant).Scan(&recordOn); err != nil {
		l.log.Warn("cdr: record policy read failed", "err", err)
	}
	if recordOn {
		ac.rec = record.New(l.dir, legID)
		ac.rec.Label(callerPID, "customer")
		// recording_url is set LATER (finalizeRecording), once a file actually
		// exists — not here. Setting it optimistically would leave a dead play
		// button ("recording not available") on calls that capture no media
		// (IVR-only, silent caller, no-answer).
	}

	l.mu.Lock()
	l.calls[roomID] = ac
	l.mu.Unlock()
	l.log.Info("cdr: call started", "room", roomID, "call", callID, "recording", recordOn)
	if ac.rec == nil {
		return nil
	}
	return ac.rec
}

// Answered marks the call answered (an agent joined) and returns the recorder
// sink to attach to the AGENT's publication (nil if not recording). The caller's
// own join is ignored (pid == callerPID), as are unknown rooms.
func (l *Log) Answered(roomID string, agentPID room.ParticipantID) room.TrackSink {
	l.mu.Lock()
	ac := l.calls[roomID]
	l.mu.Unlock()
	if ac == nil || agentPID == ac.callerPID {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.MarkCallAnswered, ac.legID); err != nil {
		l.log.Warn("cdr: mark answered failed", "err", err)
	}
	if ac.rec == nil {
		return nil
	}
	ac.rec.Label(agentPID, "agent")
	return ac.rec
}

// Journey stamps the IVR walk onto the call (calls.ivr_path) — the console's
// per-call journey view. Best-effort.
func (l *Log) Journey(roomID string, steps []ivr.Step) {
	l.mu.Lock()
	ac := l.calls[roomID]
	l.mu.Unlock()
	if ac == nil || len(steps) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.SetCallIVRPath, steps, ac.legID); err != nil {
		l.log.Warn("cdr: journey stamp failed", "err", err)
	}
}

// Ended settles the call (status ended, duration, cause) and finalizes the
// recording in the background (merge stereo → spool → optional S3). Idempotent —
// the first call wins.
func (l *Log) Ended(roomID, cause string) {
	l.mu.Lock()
	ac := l.calls[roomID]
	delete(l.calls, roomID)
	l.mu.Unlock()
	if ac == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	duration := int(time.Since(ac.startedAt).Seconds())
	if _, err := l.db.Pool.Exec(ctx, l.q.SettleCall, "ended", duration, cause, ac.legID); err != nil {
		l.log.Warn("cdr: settle failed", "room", roomID, "err", err)
	}
	l.log.Info("cdr: call ended", "room", roomID, "call", ac.id, "duration_s", duration)
	if ac.rec != nil {
		go l.finalizeRecording(ac)
	}
}

// finalizeRecording closes the per-leg captures and produces the single served
// file <dir>/<legID>.ogg: agent-left/customer-right stereo when both legs exist,
// or the customer's leg as-is for an IVR-only call. Uploads to S3 when enabled.
func (l *Log) finalizeRecording(ac *activeCall) {
	_ = ac.rec.Close()
	tracks := ac.rec.Tracks()
	var agent, customer string
	for _, t := range tracks {
		switch t.Role {
		case "agent":
			agent = t.Path
		case "customer":
			customer = t.Path
		}
	}
	if customer == "" {
		l.log.Info("cdr: no media captured, skipping recording", "call", ac.id)
		return
	}

	out := filepath.Join(l.dir, ac.legID+".ogg")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if agent != "" {
		if err := record.MergeStereo(ctx, out, agent, customer); err != nil {
			l.log.Error("cdr: stereo merge failed, keeping customer leg", "err", err)
			if err := os.Rename(customer, out); err != nil {
				l.log.Error("cdr: recording finalize failed", "err", err)
				return
			}
		} else {
			_ = os.Remove(agent)
			_ = os.Remove(customer)
		}
	} else {
		// IVR-only call: the caller's leg IS the recording.
		if err := os.Rename(customer, out); err != nil {
			l.log.Error("cdr: recording finalize failed", "err", err)
			return
		}
	}

	// The file is on disk now → publish the serve URL. Doing it here (not at
	// Started) means a call with no captured media never advertises a recording.
	if _, err := l.db.Pool.Exec(ctx, l.q.SetCallRecording, ac.legID); err != nil {
		l.log.Warn("cdr: set recording url failed", "err", err)
	}

	if l.blob.Enabled() {
		key := l.blob.Key(ac.tenant.String(), ac.startedAt, ac.legID+".ogg")
		if err := l.blob.PutFile(ctx, key, out, "audio/ogg"); err != nil {
			l.log.Error("cdr: s3 upload failed, recording stays local", "err", err)
		} else {
			if _, err := l.db.Pool.Exec(ctx, l.q.SetCallRecordingS3, ac.legID, key); err != nil {
				l.log.Warn("cdr: stamp s3 key failed", "err", err)
			} else {
				_ = os.Remove(out)
			}
		}
	}
	l.log.Info("cdr: recording finalized", "call", ac.id, "file", fmt.Sprintf("%s.ogg", ac.legID))
}
