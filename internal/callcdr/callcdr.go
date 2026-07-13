// Package callcdr records media-plane calls (web /rtc + the PSTN plug) into the
// SAME `calls` table, recording pipeline, and serve endpoint the FreeSWITCH path
// uses — one CDR + one recording story for every call source, per the
// convergence plan (docs/ARCHITECTURE.md).
//
// The existing machinery keys everything on `calls.leg_id` as an opaque per-call
// leg id and serves local recordings from <recordingsDir>/<leg_id>.<format>.
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
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/record"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/queries"
)

// Log is the media-plane call logger. One instance per gateway; safe for
// concurrent calls.
type Log struct {
	db     *db.DB
	q      *queries.Queries
	blob   *blobstore.Store // nil/disabled → recordings stay on local disk
	dir    string           // local recordings spool (shared with the API's serve)
	log    *slog.Logger
	events events.Sink // nil = events disabled (no-op)

	mu    sync.Mutex
	calls map[string]*activeCall // by room id
}

type activeCall struct {
	id        uuid.UUID // calls.id
	legID     string    // stored in calls.leg_id; names the recording file
	tenant    uuid.UUID
	startedAt time.Time
	callerPID room.ParticipantID
	rec       *record.Recorder // nil = tenant doesn't record
	answered  bool             // an agent has joined — gates RingStop's revert-to-ivr
}

// New builds a Log. blob may be nil (S3 off); dir is the recordings spool;
// pub may be nil (events disabled).
func New(d *db.DB, blob *blobstore.Store, dir string, logger *slog.Logger, pub events.Sink) *Log {
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
		db:     d,
		q:      queries.MustLoad(),
		blob:   blob,
		dir:    dir,
		log:    logger.With("component", "callcdr"),
		events: pub,
		calls:  map[string]*activeCall{},
	}
}

// Started opens the CDR for a call (status ringing) and, when the tenant records
// calls, returns the recorder as a TrackSink to attach to the CALLER's
// publication (nil → not recording). callerPID is labeled "customer". The
// recorder is created DISARMED, so the caller's pre-answer audio (IVR menus,
// ringing, queue hold) is dropped — capture actually starts when an agent
// answers (Answered → rec.Arm). source is the call's channel ('web' | 'phone'),
// recorded for direct channel reporting. Best-effort: a DB failure logs and
// returns nil — the call proceeds unrecorded rather than failing.
func (l *Log) Started(ctx context.Context, tenant uuid.UUID, roomID, from, to, source, clientRef string, callerPID room.ParticipantID) room.TrackSink {
	if from == "" {
		from = "Unknown" // never log a blank caller (e.g. a phone leg with no caller-ID)
	}
	legID := uuid.NewString()
	var callID uuid.UUID
	err := l.db.Pool.QueryRow(ctx, l.q.CreateCallInbound, tenant, from, to, legID, clientRef, source, roomID).Scan(&callID)
	if err != nil {
		l.log.Error("cdr: create call failed", "room", roomID, "err", err)
		return nil
	}
	return l.started(ctx, tenant, callID, roomID, from, to, source, "inbound", legID, callerPID)
}

// StartedOutbound is Started's sibling for calls the AGENT originates (an
// agent, already in a room, dials out over the phone plug — see
// media/signaling/outbound.go). It records `direction='outbound'` in both the
// `calls` row and the live-dashboard `ringingEvent`, instead of Started's
// hardcoded 'inbound' — that mislabeling was the bug this method fixes. It
// inserts via CreateCallOutbound (queries.sql: tenant_id, from_number,
// to_number, agent_id, leg_id, job_uuid — source is hardcoded 'phone' in the
// query itself, so there is no source param). agent_id and job_uuid are
// FreeSWITCH/originate-path concepts that don't exist yet at media-plane call
// open: agent_id is left NULL here (the answering agent is attributed later,
// same as Started, via Answered → SetCallAgentID) and job_uuid is NULLIF'd
// empty. room_id IS now persisted (CreateCallOutbound's $7) so an
// agent-initiated outbound call stays reachable by get-supervise-target —
// this was the bug that made outbound calls unsupervisable. clientRef, by
// contrast, is a genuine no-op here: CreateCallOutbound has no client_ref
// column (unlike CreateCallInbound), so the param is accepted for interface
// symmetry with Started but silently dropped — not a bug, just an unused
// knob until/unless outbound gets its own client_ref column.
func (l *Log) StartedOutbound(ctx context.Context, tenant uuid.UUID, roomID, from, to, clientRef string, callerPID room.ParticipantID) room.TrackSink {
	if from == "" {
		from = "Unknown"
	}
	legID := uuid.NewString()
	var (
		callID                                    uuid.UUID
		outTenant                                 uuid.UUID
		direction, status, fromOut, toOut, srcOut string
		agentID                                   *uuid.UUID
		legIDOut                                  string
		startedAt                                 time.Time
	)
	err := l.db.Pool.QueryRow(ctx, l.q.CreateCallOutbound, tenant, from, to, (*uuid.UUID)(nil), legID, "", roomID).
		Scan(&callID, &outTenant, &direction, &status, &fromOut, &toOut, &agentID, &legIDOut, &startedAt, &srcOut)
	if err != nil {
		l.log.Error("cdr: create outbound call failed", "room", roomID, "err", err)
		return nil
	}
	return l.started(ctx, tenant, callID, roomID, from, to, "phone", "outbound", legID, callerPID)
}

// started centralizes the recorder/registry/event bookkeeping shared by
// Started and StartedOutbound once the calls-table row already exists.
func (l *Log) started(ctx context.Context, tenant uuid.UUID, callID uuid.UUID, roomID, from, to, source, direction, legID string, callerPID room.ParticipantID) room.TrackSink {
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
	l.log.Info("cdr: call started", "room", roomID, "call", callID, "direction", direction, "recording", recordOn)
	if l.events != nil {
		l.events.Publish(ctx, tenant.String(), events.TypeCallRinging, ringingEvent(callID, from, to, source, direction, ac.startedAt))
	}
	if ac.rec == nil {
		return nil
	}
	return ac.rec
}

// Answered marks the call answered (an agent joined), attributes the answering
// agent on the CDR (resolved from their SIP username), and returns the recorder
// sink to attach to the AGENT's publication (nil if not recording). The caller's
// own join is ignored (pid == callerPID), as are unknown rooms. agentSip may be
// empty (then only status/answered_at are stamped, no agent attribution).
func (l *Log) Answered(roomID string, agentPID room.ParticipantID, agentSip string) room.TrackSink {
	l.mu.Lock()
	ac := l.calls[roomID]
	if ac != nil && agentPID != ac.callerPID {
		ac.answered = true // gate RingStop's revert-to-ivr (a late cleanup mustn't undo "talking")
	}
	l.mu.Unlock()
	if ac == nil || agentPID == ac.callerPID {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.MarkCallAnswered, ac.legID); err != nil {
		l.log.Warn("cdr: mark answered failed", "err", err)
	}
	// Attribute the answering agent: resolve their DB id from the SIP username the
	// JWT carried, then stamp it on the call log (so "who handled it" isn't blank).
	var answeredAgentID *uuid.UUID
	var answeredAgentName string
	if agentSip != "" {
		var agentID uuid.UUID
		var label string
		if err := l.db.Pool.QueryRow(ctx, l.q.GetAgentBySIP, ac.tenant, agentSip).Scan(&agentID, &label); err != nil {
			l.log.Warn("cdr: agent lookup failed", "sip", agentSip, "err", err)
		} else if _, err := l.db.Pool.Exec(ctx, l.q.SetCallAgentID, ac.legID, agentID); err != nil {
			l.log.Warn("cdr: stamp agent failed", "err", err)
		} else {
			answeredAgentID = &agentID
			answeredAgentName = label
		}
	}
	if l.events != nil {
		l.events.Publish(ctx, ac.tenant.String(), events.TypeCallAnswered, answeredEvent(ac.id, answeredAgentID, answeredAgentName, time.Now()))
	}
	if ac.rec == nil {
		return nil
	}
	ac.rec.Label(agentPID, "agent")
	// Recording begins HERE, at answer — not at call start. The caller's leg was
	// attached as a sink on Started but the recorder dropped its packets until now
	// (Arm), so the IVR/ringing/queue phase is excluded and both legs (caller +
	// this agent) start on one timeline.
	ac.rec.Arm()
	return ac.rec
}

// RingStart marks a call as now ringing a specific target (an agent or a team)
// and publishes it to the live dashboard so the row shows "ringing → <target>"
// instead of the stale IVR label. Called when a transfer node starts routing.
// The target label is resolved for humans: a team by its name, a single agent by
// their display name (falling back to the SIP username), or "Available agents"
// for a ring-all. Best-effort + dashboard-only (no calls.status change); a nil
// events sink or unknown room is a no-op.
func (l *Log) RingStart(ctx context.Context, roomID, team, agentSIP string) {
	l.mu.Lock()
	ac := l.calls[roomID]
	l.mu.Unlock()
	if ac == nil || l.events == nil {
		return
	}
	target := "Available agents"
	switch {
	case team != "":
		target = team
	case agentSIP != "":
		target = agentSIP // fall back to the SIP username if the name lookup fails
		var id uuid.UUID
		var name string
		if err := l.db.Pool.QueryRow(ctx, l.q.GetAgentBySIP, ac.tenant, agentSIP).Scan(&id, &name); err == nil && name != "" {
			target = name
		}
	}
	l.events.Publish(ctx, ac.tenant.String(), events.TypeCallRouting, routingEvent(ac.id, "ringing", target))
}

// RingStop reverts a call's live phase back to "ivr" when a ring ends with no
// answer (the caller falls back into the flow — a menu, a no-answer prompt, or
// hangup). It is a no-op once the call has been answered, so a late transfer
// cleanup can't undo the "talking" state, and on an unknown/ended room. The
// calls.status column is untouched (dashboard-only), matching RingStart.
func (l *Log) RingStop(roomID string) {
	l.mu.Lock()
	ac := l.calls[roomID]
	answered := ac != nil && ac.answered
	l.mu.Unlock()
	if ac == nil || answered || l.events == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	l.events.Publish(ctx, ac.tenant.String(), events.TypeCallRouting, routingEvent(ac.id, "ivr", ""))
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
	if l.events != nil {
		l.events.Publish(ctx, ac.tenant.String(), events.TypeCallEnded, endedEvent(ac.id, duration, cause, time.Now()))
	}
	if ac.rec != nil {
		go l.finalizeRecording(ac)
	}
}

// SuperviseStart records a supervisor session start and returns its id ("" on
// failure — the caller treats "" as "no audit row"). userID is the console
// user's id (the JWT subject) — a FK into users(id) — not a token jti.
func (l *Log) SuperviseStart(ctx context.Context, tenant uuid.UUID, callRoomID, userID, mode string) string {
	var uid *uuid.UUID
	if u, err := uuid.Parse(userID); err == nil {
		uid = &u
	}
	var callID *uuid.UUID
	l.mu.Lock()
	if ac := l.calls[callRoomID]; ac != nil {
		callID = &ac.id
	}
	l.mu.Unlock()
	var sessionID uuid.UUID
	if err := l.db.Pool.QueryRow(ctx, l.q.CreateSupervisorSession, tenant, callID, uid, []string{mode}).Scan(&sessionID); err != nil {
		l.log.Warn("supervise: start audit failed", "err", err)
		return ""
	}
	return sessionID.String()
}

// SuperviseEnd stamps the end + the full mode list on a supervisor session.
func (l *Log) SuperviseEnd(sessionID string, modes []string) {
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.EndSupervisorSession, id, modes); err != nil {
		l.log.Warn("supervise: end audit failed", "err", err)
	}
}

func ringingEvent(callID uuid.UUID, from, to, source, direction string, startedAt time.Time) map[string]any {
	return map[string]any{
		"id":          callID,
		"status":      "ringing",
		"phase":       openPhase(direction),
		"direction":   direction,
		"from_number": from,
		"to_number":   to,
		"source":      source,
		"started_at":  startedAt,
	}
}

// openPhase is the live-dashboard phase a call is in the instant it opens. An
// inbound call lands in the IVR (greeting/menu) before any agent is rung — so it
// is "ivr", NOT "ringing" (the old, misleading label). An outbound call the agent
// placed is genuinely dialing the far end, so "ringing" is correct there.
func openPhase(direction string) string {
	if direction == "outbound" {
		return "ringing"
	}
	return "ivr"
}

// routingEvent carries a live phase transition for an in-flight call: "ringing"
// (with the target agent/team being rung) when a transfer starts, or "ivr" when
// a ring ends with no answer and the caller falls back into the flow. It is a
// dashboard-only signal (best-effort, not persisted) — the calls.status column
// stays 'ringing' throughout, per the live-events-only scope.
func routingEvent(callID uuid.UUID, phase, target string) map[string]any {
	return map[string]any{
		"call_id": callID,
		"phase":   phase,
		"target":  target,
	}
}

func answeredEvent(callID uuid.UUID, agentID *uuid.UUID, agentName string, at time.Time) map[string]any {
	return map[string]any{
		"call_id":     callID,
		"agent_id":    agentID,
		"agent_name":  agentName,
		"answered_at": at,
	}
}

func endedEvent(callID uuid.UUID, duration int, cause string, at time.Time) map[string]any {
	return map[string]any{
		"call_id":          callID,
		"ended_at":         at,
		"duration_seconds": duration,
		"hangup_cause":     cause,
	}
}

// finalizeRecording closes the per-leg captures and produces the single served
// file <dir>/<legID>.ogg: agent-left/customer-right stereo when both legs exist,
// or the customer's leg as-is for an IVR-only call. Uploads to S3 when enabled.
func (l *Log) finalizeRecording(ac *activeCall) {
	_ = ac.rec.Close()
	tracks := ac.rec.Tracks()
	var customer string
	var agents []record.AgentLeg // one per agent leg; a blind transfer yields >1
	for _, t := range tracks {
		switch t.Role {
		case "agent":
			agents = append(agents, record.AgentLeg{Path: t.Path, Delay: t.Offset})
		case "customer":
			customer = t.Path
		}
	}
	if customer == "" {
		l.log.Info("cdr: no media captured, skipping recording", "call", ac.id)
		return
	}
	// Earliest-answering agent first, so the offsets read left-to-right on the timeline.
	sort.Slice(agents, func(i, j int) bool { return agents[i].Delay < agents[j].Delay })

	out := filepath.Join(l.dir, ac.legID+".ogg")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	switch {
	case len(agents) == 0:
		// IVR-only call: the caller's leg IS the recording.
		if err := os.Rename(customer, out); err != nil {
			l.log.Error("cdr: recording finalize failed", "err", err)
			return
		}
	default:
		// One agent → agent L / customer R. More than one (blind transfer) → every
		// agent laid onto L at its real offset so no leg is lost or misaligned.
		var mergeErr error
		if len(agents) == 1 {
			mergeErr = record.MergeStereo(ctx, out, agents[0].Path, customer)
		} else {
			mergeErr = record.MergeCall(ctx, out, customer, agents)
		}
		if mergeErr != nil {
			l.log.Error("cdr: stereo merge failed, keeping customer leg", "err", mergeErr, "agents", len(agents))
			if err := os.Rename(customer, out); err != nil {
				l.log.Error("cdr: recording finalize failed", "err", err)
				return
			}
		} else {
			for _, a := range agents {
				_ = os.Remove(a.Path)
			}
			_ = os.Remove(customer)
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
