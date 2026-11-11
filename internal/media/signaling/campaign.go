package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/shridarpatil/tring/internal/campaign"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/logging"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/webhook"
)

// CampaignStore is the durable boundary between scheduling and carrier effects.
type CampaignStore interface {
	Claim(context.Context, uuid.UUID, uuid.UUID) (campaign.Attempt, bool, error)
	SetCall(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	Answered(context.Context, uuid.UUID, uuid.UUID) error
	Heartbeat(context.Context, uuid.UUID, uuid.UUID) error
	Finish(context.Context, uuid.UUID, uuid.UUID, string, string) error
}

func (s *Server) handleControlCampaign(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.controlTenant(w, r)
	if !ok {
		return
	}
	var req struct {
		AttemptID uuid.UUID `json:"attempt_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.AttemptID == uuid.Nil {
		controlError(w, 400, "invalid attempt_id")
		return
	}
	if s.cfg.Campaigns == nil || s.cfg.Calls == nil || s.cfg.Originator == nil || s.cfg.Trunks == nil {
		controlError(w, 503, "campaign calling unavailable")
		return
	}
	if !s.startPortCall() {
		controlError(w, 503, "gateway restarting")
		return
	}
	a, claimed, err := s.cfg.Campaigns.Claim(r.Context(), tenant, req.AttemptID)
	if err != nil {
		s.endPortCall()
		controlError(w, 503, "could not claim campaign attempt")
		return
	}
	if claimed {
		go s.runCampaign(tenant, a)
	} else {
		s.endPortCall()
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) runCampaign(tenant uuid.UUID, a campaign.Attempt) {
	defer s.endPortCall()
	cfg := a.Payload.Config
	agentWait := 0
	if cfg.Type == "agent" {
		agentWait = cfg.AgentWaitSeconds
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(agentWait+cfg.RingSeconds+cfg.MaxSeconds+10)*time.Second)
	defer cancel()
	// Stop media if durable ownership cannot be renewed. A stale attempt is
	// classified unknown by recovery and is never silently dialed again.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cctx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := s.cfg.Campaigns.Heartbeat(cctx, tenant, a.ID)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	outcome, reason := "failed", "system_error"
	rid := room.RoomID(tenant.String() + "/campaign-" + a.ID.String())
	answered := false
	var callID uuid.UUID
	defer func() {
		if answered {
			s.cfg.Calls.Ended(string(rid), "campaign_"+outcome)
		} else {
			s.cfg.Calls.Failed(string(rid), reason)
		}
		cctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := s.cfg.Campaigns.Finish(cctx, tenant, a.ID, outcome, reason); err != nil {
			s.log.Error("campaign outcome persistence failed", "attempt", a.ID, "err", err)
			return
		}
		if hook, err := uuid.Parse(cfg.WebhookID); err == nil && s.cfg.Webhooks != nil {
			s.cfg.Webhooks.Send(tenant, hook, webhook.EventCampaignAttempt, map[string]any{"campaign_id": a.CampaignID, "recipient_id": a.RecipientID, "attempt_id": a.ID, "call_id": callID, "status": outcome, "reason": reason, "answered": answered, "external_ref": a.ExternalRef, "test": a.RecipientID == nil})
		}
	}()
	// Validate all prepared media before starting a billable call.
	files := []string{}
	if a.Payload.Prepared.AudioFile != "" {
		files = append(files, a.Payload.Prepared.AudioFile)
	} else if f := a.Payload.Prepared.Flow; f != nil {
		for _, n := range f.Nodes {
			if file := ivr.StringFromConfig(n.Config, "audio_file"); file != "" {
				files = append(files, file)
			}
		}
	} else if cfg.Type != "agent" || a.Payload.Prepared.Team != cfg.Team || cfg.Team == "" {
		reason = "missing_content"
		return
	}
	for _, file := range files {
		frames, err := loadOggFile(s.cfg.AudioDir, file)
		if err != nil || len(frames) == 0 {
			reason = "invalid_audio"
			return
		}
	}
	s.cfg.Calls.StartedOutbound(ctx, tenant, string(rid), cfg.CallerID, a.Phone, a.ExternalRef, "")
	callID = s.cfg.Calls.CallID(string(rid))
	if callID == uuid.Nil {
		reason = "call_log_unavailable"
		return
	}
	if err := s.cfg.Campaigns.SetCall(ctx, tenant, a.ID, callID); err != nil {
		reason = "call_log_unavailable"
		return
	}
	if cfg.Type == "agent" {
		outcome, reason, answered = s.runAgentCampaign(ctx, tenant, rid, a)
		return
	}
	ringCtx, stopRing := context.WithTimeout(ctx, time.Duration(cfg.RingSeconds)*time.Second)
	leg, _, _, err := s.originateOut(ringCtx, tenant, a.Phone, cfg.CallerID, nil)
	stopRing()
	if err != nil {
		outcome, reason = campaignDialOutcome(err)
		return
	}
	answered = true
	pid := leg.Source.ID()
	defer func() {
		s.cfg.Calls.Ended(string(rid), "campaign_"+outcome)
		_ = s.cfg.Originator.HangupOutbound(pid)
		s.endPstnRoom(rid)
		s.sfu.DetachPort(rid, pid)
		s.mu.Lock()
		delete(s.campaignLegs, rid)
		s.mu.Unlock()
		logging.ClearParticipant(string(pid))
	}()
	if closed(leg.Done) {
		outcome, reason = "incomplete", "recipient_hangup"
		return
	}
	s.mu.Lock()
	s.pstnCalls[rid] = room.CallID(pid)
	s.campaignLegs[rid] = pid
	s.mu.Unlock()
	tagParticipant(pid, "caller")
	if err = s.sfu.AttachPort(rid, leg.Source, leg.Write); err != nil {
		reason = "media_attach_failed"
		return
	}
	if sink := s.cfg.Calls.Customer(string(rid), pid); sink != nil {
		s.sfu.AddSink(rid, pid, sink)
	}
	if err = s.cfg.Campaigns.Answered(ctx, tenant, a.ID); err != nil {
		reason = "state_unavailable"
		return
	}
	playerID := room.ParticipantID("ivr-" + uuid.NewString())
	player := prompt.NewPlayer(playerID, 0)
	tagParticipant(playerID, "ivr")
	defer logging.ClearParticipant(string(playerID))
	defer player.Close()
	defer s.sfu.RemoveSource(rid, playerID)
	if err = s.sfu.InjectSource(rid, player, webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}); err != nil {
		reason = "media_attach_failed"
		return
	}
	if sink := s.cfg.Calls.Answered(string(rid), playerID, ""); sink != nil {
		s.sfu.AddSink(rid, playerID, sink)
	}
	callCtx, stopCall := context.WithTimeout(ctx, time.Duration(cfg.MaxSeconds)*time.Second)
	defer stopCall()
	flow := a.Payload.Prepared.Flow
	if flow == nil {
		flow = &ivr.Flow{EntryNode: "message", Nodes: []ivr.Node{{ID: "message", Type: ivr.NodeGreeting, Config: map[string]any{"audio_file": a.Payload.Prepared.AudioFile}}}}
	}
	s.setEndWebhook(rid, flow.EndWebhookID)
	driver := ivrdrive.New(ivrdrive.Config{Prompts: &filePrompts{dir: s.cfg.AudioDir, player: player}, Agents: s.agentRouter, Dialer: ivrDialer{s}, DialAudio: func() string { return holdFile(s.tenantHoldMusic(rid)) }, Room: string(rid), ClientRef: a.ExternalRef, Vars: map[string]string{"caller": a.Phone, "did": cfg.CallerID, "campaign_id": a.CampaignID.String(), "recipient_id": uuidString(a.RecipientID), "attempt_id": a.ID.String(), "external_ref": a.ExternalRef}, HTTP: s.cfg.HTTP, Logger: s.log, OnNodeEvent: func(ev ivrdrive.NodeEvent) { s.sendNodeWebhook(rid, cfg.FlowID, ev) }})
	done := make(chan error, 1)
	if !s.brain.StartWithResult(callCtx, rid, flow, driver, nil, func(path []ivr.Step, err error) { s.cfg.Calls.Journey(string(rid), path); done <- err }, func() { s.clearCaller(rid) }) {
		reason = "flow_start_failed"
		return
	}
	defer s.stopIVR(rid)
	dtmf := leg.DTMF
	for {
		select {
		case err := <-done:
			if err != nil {
				outcome, reason = "incomplete", "flow_error"
			} else if callCtx.Err() != nil || closed(leg.Done) {
				outcome, reason = "incomplete", "recipient_hangup"
			} else {
				outcome, reason = "completed", ""
			}
			return
		case digit, ok := <-dtmf:
			if !ok {
				dtmf = nil
			} else {
				s.routeDTMF(rid, digit)
			}
		case <-leg.Done:
			outcome, reason = "incomplete", "recipient_hangup"
			return
		case <-callCtx.Done():
			outcome, reason = "incomplete", "call_timeout"
			return
		}
	}
}
func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
func campaignDialOutcome(err error) (string, string) {
	var failure *room.DialFailure
	if errors.As(err, &failure) {
		switch failure.Outcome {
		case "busy", "no_answer":
			return failure.Outcome, failure.Outcome
		case "temporary_failure":
			return "failed", "temporary_failure"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no_answer", "no_answer"
	}
	if errors.Is(err, errDialCallerID) {
		return "failed", "invalid_caller_id"
	}
	if errors.Is(err, errDialInvalidNumber) {
		return "failed", "invalid_number"
	}
	if errors.Is(err, errDialNoTrunk) {
		return "failed", "no_trunk"
	}
	return "failed", "originate_failed"
}
