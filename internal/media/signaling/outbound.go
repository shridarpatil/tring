package signaling

// outbound.go — agent-initiated OUTBOUND phone calls on the media plane (Task
// 4b). By the time a msgDial arrives, the agent is ALREADY a participant in a
// room (handleWS joins it before the read loop starts). This is the outbound
// mirror of pstn.go's inbound handler: instead of answering an inbound call,
// it ORIGINATES one and attaches the resulting leg into the agent's EXISTING
// room as a bidirectional port (sfu.AttachPort) — no new room is created.
// From there the call is exactly like any other: the agent's existing
// WebRTC peer hears/talks to the carrier leg. No FreeSWITCH concept crosses
// this file — only the room.Originator/HangupOutbound seam.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// Originator originates outbound PSTN calls and hangs them up. The gateway's
// fsplug plug implements it (room.Originator's Originate, plus
// HangupOutbound for the room-hangs-up-first direction).
type Originator interface {
	room.Originator
	HangupOutbound(pid room.ParticipantID) error
}

// TrunkResolver returns the ALREADY-SELECTED ordered outbound trunks for a
// tenant — the same selection internal/api's FreeSWITCH-path originate uses
// (internal/trunkselect). The first trunk's E164 is stamped as caller-ID; the
// ordered names ride as X-Tring-Trunk for carrier-side failover.
type TrunkResolver interface {
	OutboundTrunks(ctx context.Context, tenant uuid.UUID) ([]trunkselect.Trunk, error)
}

// handleAgentDial places an outbound call on behalf of an agent already in
// roomID and plugs the resulting leg into that SAME room. Run in its own
// goroutine by the read loop (msgDial) since Originate blocks until answered,
// failed, or the call ends.
func (s *Server) handleAgentDial(roomID room.RoomID, tenant uuid.UUID, agentPID room.ParticipantID, to string) {
	if s.cfg.Originator == nil || s.cfg.Trunks == nil {
		s.sendError(agentPID, "outbound calling not available")
		return
	}

	tctx, tcancel := context.WithTimeout(context.Background(), 5*time.Second)
	selected, err := s.cfg.Trunks.OutboundTrunks(tctx, tenant)
	tcancel()
	if err != nil || len(selected) == 0 {
		s.log.Warn("outbound: no trunk available", "room", roomID, "tenant", tenant, "err", err)
		s.sendError(agentPID, "no outbound trunk")
		return
	}
	callerID := selected[0].E164
	names := make([]string, len(selected))
	for i, t := range selected {
		names[i] = t.Name
	}

	leg, err := s.cfg.Originator.Originate(context.Background(), room.OriginateRequest{
		To:       to,
		CallerID: callerID,
		Trunks:   names,
	}, func(room.Progress) {
		if c := s.connFor(agentPID); c != nil {
			_ = c.send(msg{Type: msgRinging})
		}
	})
	if err != nil {
		s.log.Warn("outbound: originate failed", "room", roomID, "to", to, "err", err)
		s.sendError(agentPID, "call failed")
		return
	}

	// The agent may have left (or the room emptied) while Originate was
	// blocking on the ring. hangupOutboundIfAbandoned found no outboundLegs
	// entry back then (the leg didn't exist yet) and no-op'd — so without this
	// recheck a now-answered carrier would get attached into a dead/empty room
	// and a CDR opened AFTER the room already ended: a lingering carrier dialog
	// plus a dangling, never-settled CDR row. Check without holding a lock
	// across HangupOutbound.
	if r, ok := s.rooms.Get(roomID); !ok || len(r.Participants()) == 0 {
		s.log.Warn("outbound: agent left during ring, hanging up carrier", "room", roomID)
		_ = s.cfg.Originator.HangupOutbound(leg.Source.ID())
		return
	}

	if err := s.sfu.AttachPort(roomID, leg.Source, leg.Write); err != nil {
		s.log.Error("outbound: attach port failed", "room", roomID, "err", err)
		_ = s.cfg.Originator.HangupOutbound(leg.Source.ID())
		s.sendError(agentPID, "call failed")
		return
	}

	s.mu.Lock()
	s.outboundLegs[roomID] = leg.Source.ID()
	s.mu.Unlock()

	// Open the CDR (and the caller's recording leg when the tenant records) —
	// the same calls-table row a FreeSWITCH-path call gets. Our allocated DID
	// (callerID) is the "from"; the dialed destination is the "to" — mirrors
	// the outbound-trunk convention in internal/api's originate path.
	if s.cfg.Calls != nil {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		if sink := s.cfg.Calls.StartedOutbound(cctx, tenant, string(roomID), callerID, to, "", leg.Source.ID()); sink != nil {
			s.sfu.AddSink(roomID, leg.Source.ID(), sink)
		}
		ccancel()
	}

	s.log.Info("outbound: call attached", "room", roomID, "to", to, "caller_id", callerID)

	<-leg.Done
	s.endOutboundRoom(roomID, "call ended")
}

// endOutboundRoom tears down everything room-side after the outbound leg
// ended (the carrier hung up, or HangupOutbound was called first): notify any
// remaining participant with a terminal `ended`, settle the CDR, and forget
// the leg. Mirrors pstn.go's endPstnRoom.
func (s *Server) endOutboundRoom(roomID room.RoomID, cause string) {
	s.mu.Lock()
	pid, hadLeg := s.outboundLegs[roomID]
	delete(s.outboundLegs, roomID)
	s.mu.Unlock()
	if hadLeg {
		// Prune the room's SFU-side port publication — otherwise r.ports/r.pubs
		// keep this leg's entry forever and the sfuRoom never gets pruned (a
		// per-call leak). NOTE: endPstnRoom (inbound) has the SAME pre-existing
		// leak; left untouched here — see the task report for the shared
		// follow-up.
		s.sfu.DetachPort(roomID, pid)
	}
	if s.cfg.Calls != nil {
		s.cfg.Calls.Ended(string(roomID), cause)
	}
	if r, ok := s.rooms.Get(roomID); ok {
		for _, p := range r.Participants() {
			if c := s.connFor(p.ID()); c != nil {
				_ = c.send(msg{Type: msgEnded, Reason: cause})
			}
			s.removeParticipant(roomID, p.ID())
		}
	}
	s.log.Info("outbound: room ended", "room", roomID)
}

// hangupOutboundIfAbandoned hangs up the outbound carrier leg when its room
// has no human participants left — the agent hung up (or dropped) with
// nobody else on the call. The leg's Done then fires and endOutboundRoom
// completes the teardown. No-op for rooms with no outbound leg attached.
func (s *Server) hangupOutboundIfAbandoned(roomID room.RoomID) {
	s.mu.Lock()
	pid, isOutbound := s.outboundLegs[roomID]
	s.mu.Unlock()
	if !isOutbound {
		return
	}
	if r, ok := s.rooms.Get(roomID); ok && len(r.Participants()) > 0 {
		return // an agent is (still) on the call
	}
	s.log.Info("outbound: no one left on the call, hanging up", "room", roomID)
	if s.cfg.Originator != nil {
		_ = s.cfg.Originator.HangupOutbound(pid)
	}
}

// sendError best-effort notifies a participant's socket of an error — the
// wire protocol's generic error envelope.
func (s *Server) sendError(pid room.ParticipantID, reason string) {
	if c := s.connFor(pid); c != nil {
		_ = c.send(msg{Type: msgError, Reason: reason})
	}
}
