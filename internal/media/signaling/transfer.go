package signaling

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
)

// TransferMessageResolver resolves a tenant's synthesized "no one answered" prompt
// path (flowstore.Store implements it). nil / "" → a failed transfer hangs up the
// caller with no message (silent-degrade).
type TransferMessageResolver interface {
	TenantTransferFailedAudio(ctx context.Context, tenantID uuid.UUID) (string, error)
}

// beginTransfer marks a room as having a blind transfer ringing and returns a
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

// transferActive reports whether a blind transfer is ringing for the room. The
// caller is briefly alone during it, so routeEvent must not tear the room down.
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
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Why a blind transfer was refused before it committed. The messages are the
// transfer-failed reasons the agent SDK shows.
var (
	errTransferTarget      = errors.New("specify exactly one of to_sip or to_team")
	errTransferSelf        = errors.New("cannot transfer to yourself")
	errTransferUnavailable = errors.New("target unavailable")
	errTransferInProgress  = errors.New("transfer already in progress")
)

// handleTransfer runs an agent's msgTransfer: a refusal goes back to the agent as
// transfer-failed (the agent keeps the call); otherwise the target rings.
func (s *Server) handleTransfer(c *conn, p *room.Participant, tenant uuid.UUID, sip string, m msg) {
	ring, err := s.commitTransfer(c, p, tenant, sip, m.ToSip, m.ToTeam)
	if err != nil {
		_ = c.send(msg{Type: msgTransferFailed, Reason: err.Error()})
		return
	}
	ring()
}

// commitTransfer starts an agent's blind transfer: validate + pre-flight the
// target, then drop the initiating agent immediately (true blind). It returns
// the ring step (re-ring the routing engine into the SAME room; on no-answer
// play the tenant message + hang up), or why the transfer was refused, in which
// case nothing changed. Shared by the agent's own msgTransfer and the API's
// POST /control/transfer.
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

	// Pre-flight: if the target is entirely unreachable now, keep the agent on the
	// call rather than dropping them into a dead ring.
	if !s.targetReachable(context.Background(), tenant.String(), toSip, toTeam) {
		return nil, errTransferUnavailable
	}

	ctx, ok := s.beginTransfer(rid)
	if !ok {
		return nil, errTransferInProgress
	}

	// Commit: drop the initiating agent immediately (true blind). Send a terminal
	// `ended` BEFORE closing the socket (same discipline as endIfAlone) so the
	// agent SDK tears the leg down instead of treating the close as an unexpected
	// drop and reconnecting — a reconnect would rejoin the room (resetting the call
	// timer) and, via signalRing, prematurely resolve the transfer ring so the real
	// target never receives the handed-off call.
	_ = c.send(msg{Type: msgTransferOK})
	_ = c.send(msg{Type: msgEnded, Reason: "transferred"})
	label := p.Name()
	if label == "" {
		label = "Transferred call"
	}
	// If this call reached the agent via the IVR, it left a paused handoff. Void it
	// BEFORE the agent leaves so routeEvent treats the departure as a transfer
	// (transferActive → keep the room) instead of a handoff-end that resumes the IVR
	// and tears the caller down. See handoff.go.
	s.voidHandoff(rid)
	s.removeParticipant(rid, p.ID())

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
	return func() { s.ringTransfer(ctx, rid, tenant, sip, to, target) }, nil
}

// ringTransfer rings a committed transfer's target and, if nobody answers, plays
// the tenant's no-answer message and hangs up the stranded caller.
func (s *Server) ringTransfer(ctx context.Context, rid room.RoomID, tenant uuid.UUID, sip, to string, target ivrdrive.Target) {
	outcome, _ := s.agentRouter.RingNoHandoff(ctx, string(rid), target)
	s.endTransfer(rid) // clear the guard (idempotent; also fired if the caller hung up)

	if outcome != "completed" {
		s.playTransferFailMessage(rid, tenant)
		s.endIfAlone(rid) // hang up the stranded web caller
		// A phone caller is the port, so endIfAlone leaves it. Its IVR may still be
		// parked behind the voided handoff; stopping it hangs up the abandoned leg.
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
		return
	}
	s.publishTransferred(tenant.String(), rid, sip, to, "completed")
}

// targetReachable reports whether a transfer target has any agent that could answer
// right now: for a specific SIP, that agent must be online, not Away and not on a call; for a
// team, at least one resolved member must be. Best-effort — the ring itself is the
// source of truth, this only avoids dropping the agent into an obviously-dead ring.
func (s *Server) targetReachable(ctx context.Context, tenant, sip, team string) bool {
	free := func(sip string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		k := tenant + "|" + sip
		return s.agents[tenant][sip] != nil && s.onCall[k] == 0 && !s.away[k]
	}
	if sip != "" {
		return free(sip)
	}
	// Team: resolve members (nil resolver → any online free agent qualifies).
	if s.cfg.Teams == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for a := range s.agents[tenant] {
			if k := tenant + "|" + a; s.onCall[k] == 0 && !s.away[k] {
				return true
			}
		}
		return false
	}
	tid, err := uuid.Parse(tenant)
	if err != nil {
		return false
	}
	sips, err := s.cfg.Teams.TeamSIPs(ctx, tid, team)
	if err != nil {
		return false
	}
	for _, a := range sips {
		if free(a) {
			return true
		}
	}
	return false
}

// playTransferFailMessage plays the tenant's synthesized no-answer prompt into the
// room once (best-effort). Silent no-op when no resolver / no audio / room gone /
// load fails — the caller is hung up by the handleTransfer caller regardless.
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
	frames, err := loadOggFile(s.cfg.AudioDir, file)
	if err != nil {
		s.log.Warn("transfer: load no-answer message failed", "room", rid, "file", file, "err", err)
		return
	}
	pid := room.ParticipantID("xfermsg-" + uuid.NewString())
	player := prompt.NewPlayer(pid, 0)
	tagParticipant(pid, "xfermsg")
	if err := s.sfu.InjectSource(rid, player, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}); err != nil {
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
