// Package callcdr records media-plane calls (web /rtc + the PSTN plug) into the
// SAME `calls` table, recording pipeline, and serve endpoint the FreeSWITCH path
// uses — one CDR + one recording story for every call source, per the
// convergence plan (docs/src/content/docs/architecture/overview.md).
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
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/callrow"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/record"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/webhook"
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
	hooks  Webhooks    // nil = tenant webhooks off (no-op)

	mu    sync.Mutex
	calls map[string]*activeCall // by room id
}

type activeCall struct {
	id         uuid.UUID // calls.id
	legID      string    // stored in calls.leg_id; names the recording file
	tenant     uuid.UUID
	startedAt  time.Time
	callerPID  room.ParticipantID
	rec        *record.Recorder // nil = tenant doesn't record
	recRate    int              // tenant's recording sample rate (Hz), read at call start
	answered   bool             // an agent has joined — gates RingStop's revert-to-ivr
	ringAgents []string         // agents rung in the current window — names the no_answer event
}

// Webhooks delivers tenant-registered webhooks; *webhook.Notifier implements
// it. The call log is the one place that knows when a media-plane call is
// answered or ends, so it emits call.answered / call.ended (the api emits
// call.ringing when it places a call).
type Webhooks interface {
	Emit(ctx context.Context, tenantID uuid.UUID, eventType string, data interface{})
}

// WithWebhooks turns on call.answered / call.ended delivery and returns l for
// chaining.
func (l *Log) WithWebhooks(w Webhooks) *Log {
	l.hooks = w
	return l
}

// emitCall sends a call.* webhook carrying the call's current row, the same
// {"call": …} object the api's call.ringing carries. Best-effort: a missing
// row or a lookup error logs and skips the event.
func (l *Log) emitCall(ctx context.Context, ac *activeCall, event string) {
	if l.hooks == nil {
		return
	}
	c, err := callrow.Get(ctx, l.db, l.q, ac.tenant, ac.id)
	if err != nil {
		l.log.Warn("cdr: webhook payload lookup failed", "event", event, "call", ac.id, "err", err)
		return
	}
	l.hooks.Emit(ctx, ac.tenant, event, callrow.Payload(c))
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
	if err := l.db.Pool.QueryRow(ctx, l.q.GetTenantRecordCalls, tenant).Scan(&recordOn, &ac.recRate); err != nil {
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
	l.timelineEvent(tenant, callID, "started", map[string]any{
		"from": from, "to": to, "source": source, "direction": direction,
	})
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
	l.timelineEvent(ac.tenant, ac.id, "answered", map[string]any{
		"agent":    answeredAgentName, // "" when the SIP didn't resolve to a named agent
		"agent_id": answeredAgentID,
	})
	if l.events != nil {
		l.events.Publish(ctx, ac.tenant.String(), events.TypeCallAnswered, answeredEvent(ac.id, answeredAgentID, answeredAgentName, time.Now()))
	}
	l.emitCall(ctx, ac, webhook.EventCallAnswered)
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
	if ac != nil {
		ac.ringAgents = nil // new routing window — reset the per-agent ring roster
	}
	l.mu.Unlock()
	if ac == nil || l.events == nil {
		return
	}
	// Live dashboard label only: the team name (or a single agent's name / "Available
	// agents") drives the "ringing → X" phase chip. The durable, per-agent ring rows
	// — who the call actually rang, by id + name — are written by RingAgent as each
	// agent is popped, since a team ring can ring several agents.
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

// RingAgent records that a SPECIFIC agent is now being rung — resolving their id +
// display name (falling back to the SIP username) — so the timeline names WHO the
// call rang, not just the team. A team ring_all yields one row per agent; a
// one-at-a-time strategy yields them in sequence as each is tried. It also rosters
// the agent so a later no-answer can list who didn't pick up. Best-effort; a nil
// DB or unknown room is a no-op. Does not touch the live dashboard.
func (l *Log) RingAgent(roomID, sip string) {
	if l.db == nil || sip == "" {
		return
	}
	l.mu.Lock()
	ac := l.calls[roomID]
	l.mu.Unlock()
	if ac == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	name := sip
	var id uuid.UUID
	var label string
	if err := l.db.Pool.QueryRow(ctx, l.q.GetAgentBySIP, ac.tenant, sip).Scan(&id, &label); err == nil && label != "" {
		name = label
	}
	var roster string
	l.mu.Lock()
	if cur := l.calls[roomID]; cur != nil {
		cur.ringAgents = append(cur.ringAgents, name)
		roster = strings.Join(cur.ringAgents, ", ")
	}
	l.mu.Unlock()
	detail := map[string]any{"agent": name, "sip": sip}
	if id != uuid.Nil {
		detail["agent_id"] = id
	}
	l.timelineEvent(ac.tenant, ac.id, "ring", detail)
	// Live dashboard: replace the team label with the agent(s) actually ringing —
	// the roster grows as a ring_all pops more agents.
	if l.events != nil {
		l.events.Publish(ctx, ac.tenant.String(), events.TypeCallRouting, routingEvent(ac.id, "ringing", roster))
	}
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
	var agents []string
	if ac != nil {
		agents = ac.ringAgents
	}
	l.mu.Unlock()
	if ac == nil || answered {
		return
	}
	// Durable timeline: the whole window ended without an answer — name who it rang.
	l.timelineEvent(ac.tenant, ac.id, "no_answer", map[string]any{"agents": agents})
	if l.events != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		l.events.Publish(ctx, ac.tenant.String(), events.TypeCallRouting, routingEvent(ac.id, "ivr", ""))
	}
}

// Transferred records an agent-initiated blind transfer on the call timeline — the
// one routing moment outside the IVR walk (an agent, on a live call, hands it to
// another agent/team). It is written at INITIATION, before the target is rung, so
// the history reads in order: "Answered by A" → "Transfer → B by A" → "Ringing B" →
// "Answered by B"; the outcome is carried by those following ring/answer rows, not
// here. from/to are SIPs (to may be a team) — both resolved to display names when
// they name an agent. Best-effort; a nil DB or unknown room no-ops.
func (l *Log) Transferred(roomID, from, to string) {
	l.mu.Lock()
	ac := l.calls[roomID]
	l.mu.Unlock()
	if ac == nil {
		return
	}
	l.timelineEvent(ac.tenant, ac.id, "transfer", map[string]any{
		"from": l.resolveAgentName(ac.tenant, from),
		"to":   l.resolveAgentName(ac.tenant, to),
	})
}

// resolveAgentName maps a SIP username to an agent's display name, returning the
// input unchanged when it doesn't name an agent (a team name, an email, or an
// unknown SIP all pass through). A nil DB is a no-op passthrough.
func (l *Log) resolveAgentName(tenant uuid.UUID, s string) string {
	if l.db == nil || s == "" {
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var id uuid.UUID
	var name string
	if err := l.db.Pool.QueryRow(ctx, l.q.GetAgentBySIP, tenant, s).Scan(&id, &name); err == nil && name != "" {
		return name
	}
	return s
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
	l.timelineEvent(ac.tenant, ac.id, "ended", map[string]any{
		"cause": cause, "duration_seconds": duration,
	})
	if l.events != nil {
		l.events.Publish(ctx, ac.tenant.String(), events.TypeCallEnded, endedEvent(ac.id, duration, cause, time.Now()))
	}
	// recording_url isn't in this payload yet: the recording is merged and
	// stamped after the call ends (finalizeRecording, below).
	l.emitCall(ctx, ac, webhook.EventCallEnded)
	if ac.rec != nil {
		go l.finalizeRecording(ac)
	}
}

// SuperviseStart records a supervisor session start and returns its id ("" on
// failure — the caller treats "" as "no audit row"). userID is the console
// user's id (the JWT subject) — a FK into users(id) — not a token jti.
func (l *Log) SuperviseStart(ctx context.Context, tenant uuid.UUID, callRoomID, userID, name, mode string) string {
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
	if callID != nil {
		l.timelineEvent(tenant, *callID, "supervise", superviseStartDetail(name, mode))
	}
	return sessionID.String()
}

// SuperviseMode records a live supervisor mode switch (monitor→whisper→barge) on
// the call timeline. Best-effort and no-op once the call is no longer active;
// tenant is resolved from the active call, so the caller passes only the room.
func (l *Log) SuperviseMode(callRoomID, name, mode string) {
	l.mu.Lock()
	ac := l.calls[callRoomID]
	l.mu.Unlock()
	if ac == nil {
		return
	}
	l.timelineEvent(ac.tenant, ac.id, "supervise", superviseModeDetail(name, mode))
}

// SuperviseEnd stamps the end + the full mode list on the supervisor session row,
// and — while the call is still live — appends a "stopped supervising" event to
// the timeline. If the call already ended, the timeline row is skipped: a stop
// after "call ended" would be misordered, and best-effort tolerates the gap.
func (l *Log) SuperviseEnd(sessionID, callRoomID, name string, modes []string) {
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.EndSupervisorSession, id, modes); err != nil {
		l.log.Warn("supervise: end audit failed", "err", err)
	}
	l.mu.Lock()
	ac := l.calls[callRoomID]
	l.mu.Unlock()
	if ac != nil {
		l.timelineEvent(ac.tenant, ac.id, "supervise", superviseEndDetail(name))
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

// superviseStartDetail / superviseModeDetail / superviseEndDetail build the
// call_events detail for the three supervisor moments. All share the "supervise"
// type; the "action" field distinguishes start / live mode switch / stop, and
// every moment carries the supervisor's display name so the timeline reads
// without a users join and disambiguates concurrent supervisors.
func superviseStartDetail(name, mode string) map[string]any {
	return map[string]any{"action": "start", "mode": mode, "supervisor": name}
}

func superviseModeDetail(name, mode string) map[string]any {
	return map[string]any{"action": "mode", "mode": mode, "supervisor": name}
}

func superviseEndDetail(name string) map[string]any {
	return map[string]any{"action": "end", "supervisor": name}
}

// timelineEvent appends one row to a call's durable event timeline (call_events)
// — the console's per-call history. Best-effort: a DB failure logs and moves on,
// leaving a gap rather than failing the call. detail is type-specific JSON.
func (l *Log) timelineEvent(tenant, callID uuid.UUID, typ string, detail map[string]any) {
	if l.db == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := l.db.Pool.Exec(ctx, l.q.InsertCallEvent, tenant, callID, typ, raw); err != nil {
		l.log.Warn("cdr: call event insert failed", "type", typ, "err", err)
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
		// IVR-only call: the caller's leg IS the recording, re-encoded at the
		// tenant's sample rate (kept as captured if ffmpeg fails).
		if err := record.Encode(ctx, out, customer, ac.recRate); err != nil {
			l.log.Error("cdr: recording encode failed, keeping capture", "err", err)
			if err := os.Rename(customer, out); err != nil {
				l.log.Error("cdr: recording finalize failed", "err", err)
				return
			}
		} else {
			_ = os.Remove(customer)
		}
	default:
		// One agent → agent L / customer R. More than one (blind transfer) → every
		// agent laid onto L at its real offset so no leg is lost or misaligned.
		var mergeErr error
		if len(agents) == 1 {
			mergeErr = record.MergeStereo(ctx, out, agents[0].Path, customer, ac.recRate)
		} else {
			mergeErr = record.MergeCall(ctx, out, customer, agents, ac.recRate)
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
