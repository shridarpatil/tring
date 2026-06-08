package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Call control (hold/resume, and later transfer/conference) is BACKEND-MEDIATED:
// the CRM's backend (tenant API key) calls these on the agent's behalf, exactly
// like origination (POST /v1/calls). Per ADR-001 the agent SDK holds no API key
// and never calls Tring directly — its widget fires a callback the CRM wires to
// its backend, which hits these endpoints.

// handleHoldCall puts the customer on hold (they hear hold music). It holds the
// AGENT leg, so the bridged customer hears MOH while the agent steps away.
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

	var leg, status string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetCallControlLeg, id, t.ID).Scan(&leg, &status)
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

	// uuid_hold <uuid> holds; "off <uuid>" resumes. The held leg's bridged peer
	// (the customer) hears MOH.
	args := leg
	if !hold {
		args = "off " + leg
	}
	if _, err := s.fs.Command(r.Context(), "uuid_hold", args); err != nil {
		s.log.Error("uuid_hold failed", "call", id, "hold", hold, "err", err)
		writeError(w, http.StatusBadGateway, "hold command failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id.String(), "held": hold})
}
