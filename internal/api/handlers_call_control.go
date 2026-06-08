package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// holdLeg points the held leg's hold_music at the tenant's configured MOH (when
// set) and issues uuid_hold / resume. uuid_hold reads hold_music at hold time,
// so it's set first; a failure there just falls back to the FS default.
func (s *Server) holdLeg(ctx context.Context, leg, holdMusic string, hold bool) error {
	if hold && holdMusic != "" {
		if _, err := s.fs.Command(ctx, "uuid_setvar", leg+" hold_music "+holdMusic); err != nil {
			s.log.Warn("set hold_music failed, using default", "err", err)
		}
	}
	args := leg
	if !hold {
		args = "off " + leg
	}
	_, err := s.fs.Command(ctx, "uuid_hold", args)
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
