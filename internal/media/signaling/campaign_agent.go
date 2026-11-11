package signaling

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/room"
)

// runAgentCampaign uses the normal team router and its shared agent reservations.
// No carrier call is made until an invited team agent has accepted the call.
func (s *Server) runAgentCampaign(ctx context.Context, tenant uuid.UUID, rid room.RoomID, a campaign.Attempt) (status, reason string, answered bool) {
	cfg := a.Payload.Config
	if s.cfg.Teams == nil || cfg.Team == "" || a.Payload.Prepared.Team != cfg.Team {
		return "failed", "invalid_team", false
	}
	lookupCtx, stopLookup := context.WithTimeout(ctx, 5*time.Second)
	_, exists, err := s.cfg.Teams.TeamConfig(lookupCtx, tenant, cfg.Team)
	stopLookup()
	if err != nil || !exists {
		return "failed", "invalid_team", false
	}
	// Reserve the room before any agent joins: joining must neither dial a
	// second client nor mark the customer answered in the call log.
	release, ok := s.beginDial(rid, "campaign:"+a.ID.String())
	if !ok {
		return "failed", "already_dialing", false
	}
	defer release()
	var clientPID room.ParticipantID
	defer func() {
		// Settle before removing the agent; their leave also settles the CDR.
		if answered {
			s.cfg.Calls.Ended(string(rid), "campaign_"+status)
		} else {
			s.cfg.Calls.Failed(string(rid), reason)
		}
		if clientPID != "" {
			_ = s.cfg.Originator.HangupOutbound(clientPID)
		}
		s.endOutboundRoom(rid, "campaign_"+status)
		if clientPID != "" {
			s.sfu.DetachPort(rid, clientPID)
			logging.ClearParticipant(string(clientPID))
		}
	}()

	waitCtx, stopWait := context.WithTimeout(ctx, time.Duration(cfg.AgentWaitSeconds)*time.Second)
	result, err := s.agentRouter.RingNoHandoff(waitCtx, string(rid), ivrdrive.Target{Team: cfg.Team, Label: a.Phone})
	stopWait()
	if err != nil || result != "completed" || ctx.Err() != nil {
		return "no_answer", "agent_not_answered", false
	}
	pids := s.agentPIDs(rid)
	if len(pids) != 1 {
		return "failed", "agent_hangup", false
	}
	agentPID := pids[0]
	s.mu.Lock()
	agent, present := s.agentLegs[agentPID]
	s.mu.Unlock()
	if !present || agent.p.State() == room.StateLeft {
		return "failed", "agent_hangup", false
	}
	// Observe agent departure during the customer ring AND the conversation.
	callCtx, stop := context.WithCancel(ctx)
	defer stop()
	go s.cancelWhenEmpty(callCtx, rid, stop)
	stopMusic := s.playHold(rid, s.tenantHoldMusic(rid))
	defer stopMusic()
	ringCtx, stopRing := context.WithTimeout(callCtx, time.Duration(cfg.RingSeconds)*time.Second)
	leg, _, _, err := s.originateOut(ringCtx, tenant, a.Phone, cfg.CallerID, func(room.Progress) {
		if c := s.connFor(agentPID); c != nil {
			_ = c.send(msg{Type: msgRinging})
		}
	})
	stopRing()
	stopMusic()
	if err != nil {
		if callCtx.Err() != nil || agent.p.State() == room.StateLeft {
			return "failed", "agent_hangup", false
		}
		status, reason = campaignDialOutcome(err)
		return status, reason, false
	}
	clientPID = leg.Source.ID()
	// Carrier answer is distinct from a successfully connected conversation.
	if err := s.cfg.Campaigns.Answered(ctx, tenant, a.ID); err != nil {
		return "incomplete", "state_unavailable", true
	}
	if callCtx.Err() != nil || agent.p.State() == room.StateLeft || closed(leg.Done) {
		return "incomplete", "disconnected_before_bridge", true
	}
	tagParticipant(clientPID, "callee")
	if err := s.sfu.AttachPort(rid, leg.Source, leg.Write); err != nil {
		return "incomplete", "media_attach_failed", true
	}
	s.mu.Lock()
	s.outboundLegs[rid] = clientPID
	s.mu.Unlock()
	if sink := s.cfg.Calls.Customer(string(rid), clientPID); sink != nil {
		s.sfu.AddSink(rid, clientPID, sink)
	}
	if sink := s.cfg.Calls.Answered(string(rid), agentPID, agent.sip); sink != nil {
		s.sfu.AddSink(rid, agentPID, sink)
	}
	if c := s.connFor(agentPID); c != nil {
		_ = c.send(msg{Type: msgAnswered})
	}
	timer := time.NewTimer(time.Duration(cfg.MaxSeconds) * time.Second)
	defer timer.Stop()
	select {
	case <-leg.Done:
		return "completed", "", true
	case <-callCtx.Done():
		if ctx.Err() != nil {
			return "incomplete", "call_interrupted", true
		}
		return "completed", "agent_hangup", true
	case <-timer.C:
		return "incomplete", "call_timeout", true
	}
}
