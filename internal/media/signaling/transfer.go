package signaling

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// TransferMessageResolver resolves a tenant's synthesized "no one answered" prompt
// path (flowstore.Store implements it). nil / "" → a failed transfer hangs up the
// caller with no message (silent-degrade).
type TransferMessageResolver interface {
	TenantTransferFailedAudio(ctx context.Context, tenantID uuid.UUID) (string, error)
}

// beginTransfer marks a room as having a transfer ringing and returns a
// context cancelled when the transfer ends (endTransfer) or the caller hangs up
// (routeEvent's empty-room branch). ok=false when a transfer is already in flight
// for the room (a second transfer is rejected).
func (s *Server) beginTransfer(rid room.RoomID) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.transfers[rid]; exists {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.transfers[rid] = cancel
	return ctx, true
}

// transferActive reports whether a transfer is ringing for the room. The caller
// may be alone during it (a team transfer, or the transferring agent hung up),
// so routeEvent must not tear the room down.
func (s *Server) transferActive(rid room.RoomID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.transfers[rid]
	return ok
}

// endTransfer clears the transfer guard and cancels its context. Idempotent —
// safe to call from both the transfer goroutine and the room-teardown path.
func (s *Server) endTransfer(rid room.RoomID) {
	s.mu.Lock()
	cancel := s.transfers[rid]
	delete(s.transfers, rid)
	delete(s.transferring, rid)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Why a transfer was refused before it committed. The messages are the
// transfer-failed reasons the agent SDK shows.
var (
	errTransferTarget      = errors.New("specify exactly one of to_sip or to_team")
	errTransferSelf        = errors.New("cannot transfer to yourself")
	errTransferUnavailable = errors.New("target unavailable")
	errTransferInProgress  = errors.New("transfer already in progress")
)

// handleTransfer runs an agent's msgTransfer: a refusal goes back to the agent as
// transfer-failed (the agent keeps the call); otherwise the target rings, or a
// team's queue takes the call.
func (s *Server) handleTransfer(c *conn, p *room.Participant, tenant uuid.UUID, sip string, m msg) {
	ring, err := s.commitTransfer(c, p, tenant, sip, m.ToSip, m.ToTeam)
	if err != nil {
		_ = c.send(msg{Type: msgTransferFailed, Reason: err.Error()})
		return
	}
	ring()
}

// defaultTransferTimeout is how long a transfer rings before the call goes
// back to the transferring agent, when the tenant's setting can't be read.
const defaultTransferTimeout = 60 * time.Second

// TransferTimeoutResolver reads a tenant's transfer timeout
// (tenants.transfer_timeout_seconds; flowstore.Store implements it).
type TransferTimeoutResolver interface {
	TenantTransferTimeout(ctx context.Context, tenantID uuid.UUID) (time.Duration, error)
}

// transferTimeout is the tenant's transfer timeout, or the default when it
// isn't configured or can't be read — a transfer must never ring unbounded.
func (s *Server) transferTimeout(tenant uuid.UUID) time.Duration {
	if s.cfg.TransferTimeouts == nil {
		return defaultTransferTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d, err := s.cfg.TransferTimeouts.TenantTransferTimeout(ctx, tenant)
	if err != nil || d <= 0 {
		if err != nil {
			s.log.Warn("transfer: read tenant timeout failed, using default", "tenant", tenant, "err", err)
		}
		return defaultTransferTimeout
	}
	return d
}

// pendingTransfer is the agent waiting on a ringing transfer, with the IVR
// handoff set aside for them (nil when the call didn't come through the IVR).
type pendingTransfer struct {
	agent   room.ParticipantID
	handoff *handoff
	// toSip is the agent a transfer to one agent rings ("" for a team).
	// declined: they declined, which ends the ring at once (pionRing.Exhausted)
	// and is the reason the waiting agent is given.
	toSip    string
	declined bool
}

// transferAgent reports the agent waiting on rid's ringing transfer, if any.
func (s *Server) transferAgent(rid room.RoomID) room.ParticipantID {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pt := s.transferring[rid]; pt != nil {
		return pt.agent
	}
	return ""
}

// commitTransfer starts an agent's transfer: validate + pre-flight the target,
// then either
//   - to an agent: keep the initiating agent on the call — cut off both ways
//     (deaf, and heard by nobody) — while the target rings, and hand the call
//     back to them if nobody answers (transfer with fallback); or
//   - to a team: hand the call to the team's queue, releasing the agent (the
//     ring step ends their leg). The caller waits in line like any other
//     caller for that team, up to its max_wait.
//
// It returns the ring step (re-ring the routing engine into the SAME room; see
// ringTransfer for answer / no-answer), or why the transfer was refused, in
// which case nothing changed. Shared by the agent's own msgTransfer and the
// API's POST /control/transfer.
func (s *Server) commitTransfer(c *conn, p *room.Participant, tenant uuid.UUID, sip, toSip, toTeam string) (func(), error) {
	rid := p.Room().ID()
	toSip, toTeam = strings.TrimSpace(toSip), strings.TrimSpace(toTeam)

	// Validate: exactly one target; not yourself.
	if (toSip == "") == (toTeam == "") {
		return nil, errTransferTarget
	}
	if toSip != "" && toSip == sip {
		return nil, errTransferSelf
	}

	// Pre-flight: keep the agent on the call rather than start a dead ring — an
	// agent who can't answer now, or a team that doesn't exist. A team with no
	// free member is fine: the caller queues for it.
	if !s.targetReachable(context.Background(), tenant.String(), toSip, toTeam) {
		return nil, errTransferUnavailable
	}

	ctx, ok := s.beginTransfer(rid)
	if !ok {
		return nil, errTransferInProgress
	}
	// Handing the call on means no wrap-up for this agent — set before the
	// ring, as hanging up mid-ring is this agent's leg ending. Cleared if the
	// call comes back to them.
	s.setLegTransferred(p.ID(), true)

	// The agent stays until someone answers, and gets the call back if nobody
	// does. Meanwhile the customer hears the ring's hold music, so the agent is
	// cut off both ways — deaf to the room, and heard by nobody — server-side,
	// which also covers a transfer started over REST and a page refresh.
	//
	// If the call came through the IVR it left a paused handoff. Set it aside:
	// the agent leaving mid-ring must read as the transfer going on, not as the
	// handoff ending (which resumes the IVR and tears the caller down). It is
	// put back if the call returns to the agent.
	s.mu.Lock()
	h := s.handoffs[rid]
	delete(s.handoffs, rid)
	s.transferring[rid] = &pendingTransfer{agent: p.ID(), handoff: h}
	s.mu.Unlock()
	if toTeam == "" {
		s.isolateAgent(rid, p.ID(), true)
	}
	_ = c.send(msg{Type: msgTransferOK})

	label := p.Name()
	if label == "" {
		label = "Transferred call"
	}
	target := ivrdrive.Target{AgentSIP: toSip, Team: toTeam, Label: label}
	to := toSip
	if to == "" {
		to = toTeam
	}
	// Record the transfer on the timeline NOW, before the target rings, so the call
	// history stays chronological: "Transfer → <target> by <agent>" lands ahead of
	// the "Ringing <target>" / "Answered by <target>" rows the ring below produces.
	if s.cfg.Calls != nil {
		s.cfg.Calls.Transferred(string(rid), sip, to)
	}
	return func() { s.ringTransfer(ctx, rid, tenant, sip, to, target, p.ID()) }, nil
}

// keepForTransfer decides, for a participant leaving a room whose transfer is
// ringing, whether the room stays up for the ring. It does when the leaver is
// the transferring agent (hanging up mid-ring turns it into a plain blind
// transfer). When someone else leaves while the agent still waits — the web
// customer hung up — the ring is stopped and the call ends instead of leaving
// the agent alone in a ringing room.
func (s *Server) keepForTransfer(rid room.RoomID, leaver room.ParticipantID) bool {
	waiting := s.transferAgent(rid)
	if waiting == "" || waiting == leaver {
		return true
	}
	s.endTransfer(rid)
	return false
}

// inRoom reports whether pid is a current member of r.
func inRoom(r *room.Room, pid room.ParticipantID) bool {
	for _, p := range r.Participants() {
		if p.ID() == pid {
			return true
		}
	}
	return false
}

// isolateAgent cuts a transferring agent off from the call (deaf, and heard by
// nobody) or restores them.
func (s *Server) isolateAgent(rid room.RoomID, pid room.ParticipantID, on bool) {
	s.setDeaf(rid, pid, on)
	s.setAudience(rid, pid, !on, nil)
}

// ringTransfer rings a committed transfer's target. A team transfer first
// releases the agent and queues the caller for the team. Answered: the waiting
// agent leaves (a terminal `ended`, so their SDK doesn't reconnect into the
// room), and an IVR handoff passes to the answering agent so the flow's
// post-call step still runs when they hang up. Not answered with the agent
// still there: the call goes back to them. Not answered with no agent to go
// back to (a team queue timed out, or the agent hung up mid-ring): the tenant's
// no-answer message plays and the stranded caller is hung up.
func (s *Server) ringTransfer(ctx context.Context, rid room.RoomID, tenant uuid.UUID, sip, to string, target ivrdrive.Target, agentPID room.ParticipantID) {
	ringCtx, cancelRing := ctx, context.CancelFunc(func() {})
	if target.Team != "" {
		// To the queue: the agent is done with this call. The caller waits
		// for the team as long as its max_wait allows.
		s.releaseAgent(rid, agentPID)
	} else {
		// The transferring agent waits, cut off from the call, for as long as
		// this rings, so it gets its own bound instead of the agent line's
		// max_wait. A separate context: running out of time must not look like
		// the call ending.
		ringCtx, cancelRing = context.WithTimeout(ctx, s.transferTimeout(tenant))
		// Before the ring starts, so a decline can't arrive ahead of it.
		s.mu.Lock()
		if pt := s.transferring[rid]; pt != nil {
			pt.toSip = target.AgentSIP
		}
		s.mu.Unlock()
	}
	outcome, _ := s.agentRouter.RingNoHandoff(ringCtx, string(rid), target)
	cancelRing()
	// Read before endTransfer below, which cancels ctx itself: cancelled here
	// means the call ended (or the transfer was stopped) while it rang.
	callEnded := ctx.Err() != nil

	s.mu.Lock()
	pt := s.transferring[rid]
	s.mu.Unlock()
	agentHere := false
	if r, ok := s.rooms.Get(rid); ok && pt != nil && pt.agent == agentPID {
		agentHere = inRoom(r, agentPID)
	}
	s.endTransfer(rid) // clear the guard (idempotent; also fired if the caller hung up)

	if outcome == "completed" {
		// The answering agent now serves the IVR handoff: when they hang up
		// with the caller still there, the flow's post-call step runs. The
		// leaving agent below doesn't resume it — the target is still on.
		s.mu.Lock()
		if pt != nil && pt.handoff != nil && s.handoffs[rid] == nil {
			s.handoffs[rid] = pt.handoff
		}
		s.mu.Unlock()
		if agentHere {
			s.releaseAgent(rid, agentPID)
		}
		s.publishTransferred(tenant.String(), rid, sip, to, "completed")
		return
	}

	if agentHere && !callEnded {
		// Nobody answered (or the target declined): the call is the agent's again.
		reason := "no answer"
		s.mu.Lock()
		if pt.handoff != nil && s.handoffs[rid] == nil {
			s.handoffs[rid] = pt.handoff
		}
		if pt.declined {
			reason = "declined"
		}
		s.mu.Unlock()
		s.isolateAgent(rid, agentPID, false)
		s.setLegTransferred(agentPID, false)
		if c := s.connFor(agentPID); c != nil {
			_ = c.send(msg{Type: msgTransferFailed, Reason: reason})
		}
		s.publishTransferred(tenant.String(), rid, sip, to, "no_answer")
		return
	}

	var parked *handoff
	if pt != nil {
		s.mu.Lock()
		parked = pt.handoff
		s.mu.Unlock()
	}
	if !callEnded && parked != nil && s.brain.Running(rid) {
		// The call came through the IVR, still parked on the Transfer node that
		// first connected it. Hand it back there with this transfer's failure
		// ("queue_timeout" when a team's max_wait ran out, else "no_answer"),
		// so the flow's own timeout / no-answer branch decides what the caller
		// hears next, instead of the generic message and a hangup. The handoff
		// was set aside for the ring, so nothing else can resume it.
		parked.outcome = outcome
		close(parked.resume)
		s.publishTransferred(tenant.String(), rid, sip, to, "no_answer")
		return
	}

	s.playTransferFailMessage(rid, tenant)
	s.endIfAlone(rid) // hang up the stranded web caller
	// A phone caller is the port, so endIfAlone leaves it. Its IVR may still be
	// parked behind the set-aside handoff; stopping it hangs up the abandoned leg.
	switch {
	case s.brain.Running(rid):
		s.stopIVR(rid)
	case s.phoneLive(rid):
		s.hangupPstnIfAbandoned(rid)
	case s.outboundLive(rid):
		// A click-to-call customer is the port too. Settle the CDR as a
		// system end first — the leg's Done would otherwise settle it as
		// callee_hangup — then hang up (endOutboundRoom finishes teardown).
		if s.cfg.Calls != nil {
			s.cfg.Calls.Ended(string(rid), "system")
		}
		s.hangupOutboundIfAbandoned(rid)
	}
	s.publishTransferred(tenant.String(), rid, sip, to, "no_answer")
}

// declineTransfer records that sip, the one agent a transfer rings, declined
// it, so the waiting agent gets the call back with reason "declined". The ring
// itself ends through pionRing.Exhausted. A team member's decline doesn't
// count: the ring just moves on.
func (s *Server) declineTransfer(rid room.RoomID, sip string) {
	s.mu.Lock()
	if pt := s.transferring[rid]; pt != nil && pt.toSip != "" && pt.toSip == sip {
		pt.declined = true
	}
	s.mu.Unlock()
}

// releaseAgent ends the transferring agent's leg: a terminal `ended`
// ("transferred", so their SDK doesn't reconnect into the room), then leave.
func (s *Server) releaseAgent(rid room.RoomID, pid room.ParticipantID) {
	if c := s.connFor(pid); c != nil {
		_ = c.send(msg{Type: msgEnded, Reason: "transferred"})
	}
	s.removeParticipant(rid, pid)
}

// targetReachable reports whether a transfer can be handed to its target: a
// specific SIP must be online, not Away, not on a call and not wrapping up; a
// team must exist (its members may all be busy — the caller queues).
// Best-effort — the ring itself is the source of truth, this only avoids an
// obviously-dead transfer.
func (s *Server) targetReachable(ctx context.Context, tenant, sip, team string) bool {
	free := func(sip string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		k := tenant + "|" + sip
		return s.agents[tenant][sip] != nil && s.onCall[k] == 0 && s.wrapUps[k] == nil && !s.away[k]
	}
	if sip != "" {
		return free(sip)
	}
	// Team: nil resolver → the queue rings any online agent.
	if s.cfg.Teams == nil {
		return true
	}
	tid, err := uuid.Parse(tenant)
	if err != nil {
		return false
	}
	_, ok, err := s.cfg.Teams.TeamConfig(ctx, tid, team)
	// A failed lookup isn't proof the team is gone: the queue runs on its
	// defaults then (brain.AgentRouter does the same).
	return err != nil || ok
}

// playTransferFailMessage plays the tenant's synthesized no-answer prompt into the
// room once (best-effort). Silent no-op when no resolver / no audio / room gone /
// load fails — ringTransfer hangs the caller up regardless.
func (s *Server) playTransferFailMessage(rid room.RoomID, tenantID uuid.UUID) {
	if s.cfg.TransferMessage == nil {
		return
	}
	file, err := s.cfg.TransferMessage.TenantTransferFailedAudio(context.Background(), tenantID)
	if err != nil || file == "" {
		return
	}
	if _, ok := s.rooms.Get(rid); !ok {
		return
	}
	codec := s.sfu.RoomCodec(rid)
	frames, err := loadAudio(&s.g711, s.cfg.AudioDir, file, codec)
	if err != nil {
		s.log.Warn("transfer: load no-answer message failed", "room", rid, "file", file, "err", err)
		return
	}
	pid := room.ParticipantID("xfermsg-" + uuid.NewString())
	player := prompt.NewPlayer(pid, codec, 0)
	tagParticipant(pid, "xfermsg")
	if err := s.sfu.InjectSource(rid, player, sfu.AudioCapability(codec)); err != nil {
		_ = player.Close()
		logging.ClearParticipant(string(pid))
		return
	}
	<-player.Play(frames) // one-shot (not looped)
	s.sfu.RemoveSource(rid, pid)
	_ = player.Close()
}

// publishTransferred emits the live call.transferred dashboard event (with the
// resolved outcome). The durable timeline row is written separately at transfer
// initiation (see handleTransfer → Calls.Transferred), not here. Best-effort.
func (s *Server) publishTransferred(tenant string, rid room.RoomID, fromSip, to, result string) {
	if s.cfg.Events == nil {
		return
	}
	_, name, _ := strings.Cut(string(rid), "/")
	s.cfg.Events.Publish(context.Background(), tenant, events.TypeCallTransferred, map[string]any{
		"room":       name,
		"from_agent": fromSip,
		"to":         to,
		"result":     result,
	})
}
