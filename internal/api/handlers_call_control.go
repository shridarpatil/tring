package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whatomate/tring/internal/agentleg"
)

// Call control (hold/resume; later transfer/conference). Two entry points reach
// the same FreeSWITCH action:
//
//   - Agent SDK, agent JWT (POST /v1/sdk/calls/hold): the agent controls their
//     OWN active call, resolved from the JWT. The widget already holds the JWT,
//     so the Hold button works with no CRM proxy.
//   - CRM backend, tenant API key (POST /v1/calls/{id}/hold): backend-mediated
//     control of any call by id — the origination-style path (ADR-001).
//
// Both hold the AGENT leg so the bridged customer hears hold music.

// defaultMOH is FreeSWITCH's looping hold-music stream (local_stream://moh,
// defined in local_stream.conf.xml) — used when the tenant configured none.
const defaultMOH = "local_stream://moh"

// holdLeg plays hold music INTO the customer leg by media injection
// (uuid_displace), so the bridged agent's audio is replaced by MOH. It does NOT
// use uuid_hold: that re-INVITEs the leg, and a media-gateway leg can't
// renegotiate — FreeSWITCH then marks the gateway DOWN and the call drops.
// Resume stops the displacement. Source = the tenant's hold music, else the
// looping default (a single uploaded file plays once; the default loops).
func (s *Server) holdLeg(ctx context.Context, leg, holdMusic string, hold bool) error {
	src := holdMusic
	if src == "" {
		src = defaultMOH
	}
	var args string
	if hold {
		args = leg + " start " + src + " 0" // 0 = no time limit
	} else {
		args = leg + " stop " + src
	}
	_, err := s.fs.Command(ctx, "uuid_displace", args)
	return err
}

// ── Agent SDK (agent JWT) — control the agent's OWN active call ──────────────

func (s *Server) handleSdkHold(w http.ResponseWriter, r *http.Request)   { s.sdkHold(w, r, true) }
func (s *Server) handleSdkResume(w http.ResponseWriter, r *http.Request) { s.sdkHold(w, r, false) }

func (s *Server) sdkHold(w http.ResponseWriter, r *http.Request, hold bool) {
	claims := agentFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no agent")
		return
	}
	agentID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad agent id")
		return
	}

	var leg, holdMusic string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgentActiveCallLeg, claims.TenantID, agentID).Scan(&leg, &holdMusic)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "no active call")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if leg == "" {
		writeError(w, http.StatusConflict, "call has no media leg yet")
		return
	}
	if err := s.holdLeg(r.Context(), leg, holdMusic, hold); err != nil {
		s.log.Error("sdk hold failed", "agent", agentID, "hold", hold, "err", err)
		writeError(w, http.StatusBadGateway, "hold command failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"held": hold})
}

type sdkTransferRequest struct {
	ToSip string `json:"to_sip"`
}

// handleSdkTransfer blind-transfers the agent's current call to another agent:
// the client is re-bridged to the target (hearing the tenant's hold music as
// ringback while it rings) and the requesting agent's leg drops. A re-bridge —
// NOT uuid_hold — avoids a SIP re-INVITE, so the media-gateway legs survive.
// Blind has no fallback: if the target can't take it the caller is dropped, so
// we require the target to be a real, available agent. Attended transfer and
// conference (3-way + everyone-recorded) come later.
func (s *Server) handleSdkTransfer(w http.ResponseWriter, r *http.Request) {
	claims := agentFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no agent")
		return
	}
	agentID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad agent id")
		return
	}
	var req sdkTransferRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	target := strings.TrimSpace(req.ToSip)
	if target == "" {
		writeError(w, http.StatusBadRequest, "to_sip required")
		return
	}

	var targetID uuid.UUID
	var label string
	if err := s.db.Pool.QueryRow(r.Context(), s.q.GetAgentBySIP, claims.TenantID, target).Scan(&targetID, &label); err != nil {
		writeError(w, http.StatusNotFound, "unknown target agent")
		return
	}
	if targetID == agentID {
		writeError(w, http.StatusBadRequest, "cannot transfer to yourself")
		return
	}
	if s.presence.Enabled() && !s.presence.Available(r.Context(), claims.TenantID.String(), target) {
		writeError(w, http.StatusConflict, "target agent is not available")
		return
	}

	// The client leg (the party to move) + the tenant's hold music for ringback.
	var leg, holdMusic string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgentActiveCallLeg, claims.TenantID, agentID).Scan(&leg, &holdMusic)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "no active call")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if leg == "" {
		writeError(w, http.StatusConflict, "call has no media leg yet")
		return
	}

	moh := holdMusic
	if moh == "" {
		moh = defaultMOH
	}
	// Single channel var (tenant header) → no comma → safe for the inline
	// dialplan. (Attribution of the transferred-to agent on the CDR needs
	// tring_agent too, which adds a comma — a follow-up via a dialplan ext.)
	dest := agentleg.Build(agentleg.Options{
		Transport: "gateway",
		Gateway:   s.agentGateway,
		SIP:       target,
		TenantID:  claims.TenantID.String(),
	})
	inline := fmt.Sprintf("set:ringback=%s,set:instant_ringback=true,bridge:%s", moh, dest)
	if _, err := s.fs.Command(r.Context(), "uuid_transfer", leg+" '"+inline+"' inline"); err != nil {
		s.log.Error("transfer failed", "agent", agentID, "to", target, "err", err)
		writeError(w, http.StatusBadGateway, "transfer command failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transferred_to": target})
}

// ── CRM backend (tenant API key) — control any call by id ────────────────────

// handleHoldCall puts the customer on hold (they hear hold music) by holding the
// agent leg.
func (s *Server) handleHoldCall(w http.ResponseWriter, r *http.Request) {
	s.setCallHold(w, r, true)
}

// handleResumeCall takes the call off hold.
func (s *Server) handleResumeCall(w http.ResponseWriter, r *http.Request) {
	s.setCallHold(w, r, false)
}

func (s *Server) setCallHold(w http.ResponseWriter, r *http.Request, hold bool) {
	t := tenantFromCtx(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var leg, status, holdMusic string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetCallControlLeg, id, t.ID).Scan(&leg, &status, &holdMusic)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if status != "answered" {
		writeError(w, http.StatusConflict, "call is not active")
		return
	}
	if leg == "" {
		writeError(w, http.StatusConflict, "call has no media leg yet")
		return
	}
	if err := s.holdLeg(r.Context(), leg, holdMusic, hold); err != nil {
		s.log.Error("uuid_hold failed", "call", id, "hold", hold, "err", err)
		writeError(w, http.StatusBadGateway, "hold command failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id.String(), "held": hold})
}
