package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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

// defaultMOH is FreeSWITCH's looping hold-music stream (local_stream://moh,
// defined in local_stream.conf.xml) — used when the tenant configured none.
const defaultMOH = "local_stream://moh"

// holdLeg plays hold music INTO the customer leg by media injection
// (uuid_displace), so the bridged agent's audio is replaced by MOH. It does NOT
// use uuid_hold: that re-INVITEs the leg, and a media-gateway leg can't
// renegotiate — FreeSWITCH then marks the gateway DOWN and the call drops.
// Resume stops the displacement. Source = the tenant's hold music, else the
// looping default. The "l" flag LOOPS the source (switch_ivr_displace_session):
// without it a single uploaded file plays once and then goes silent for the
// rest of the hold — only stream sources like local_stream://moh loop on their
// own. With "l", an uploaded file behaves like the default MOH (and the flag is
// a harmless no-op for a stream that never hits EOF). "0" = no time limit.
func (s *Server) holdLeg(ctx context.Context, leg, holdMusic string, hold bool) error {
	src := holdMusic
	if src == "" {
		src = defaultMOH
	}
	var args string
	if hold {
		args = leg + " start " + src + " 0 l"
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

// handleSdkTransferTargets lists where the agent can transfer their call: online,
// available agents (by name) and teams. Powers the widget's transfer picker.
func (s *Server) handleSdkTransferTargets(w http.ResponseWriter, r *http.Request) {
	claims := agentFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no agent")
		return
	}
	selfID, _ := uuid.Parse(claims.Subject)

	type agentTarget struct {
		Sip  string `json:"sip"`
		Name string `json:"name"`
	}
	agents := []agentTarget{}
	if rows, err := s.db.Pool.Query(r.Context(), s.q.ListAgents, claims.TenantID); err == nil {
		defer rows.Close()
		for rows.Next() {
			var a Agent
			if err := rows.Scan(&a.ID, &a.TenantID, &a.SipUsername, &a.DisplayName, &a.AvailableForCalls, &a.CreatedAt, &a.Role); err != nil {
				// Logged, not skipped silently: a column drift here once
				// emptied the picker without a trace.
				s.log.Error("transfer targets: scan agent", "err", err)
				continue
			}
			if a.ID == selfID || !a.AvailableForCalls || !s.agentReachable(r.Context(), claims.TenantID, a.SipUsername) {
				continue
			}
			name := a.DisplayName
			if name == "" {
				name = a.SipUsername
			}
			agents = append(agents, agentTarget{Sip: a.SipUsername, Name: name})
		}
	} else {
		s.log.Error("transfer targets: list agents", "err", err)
	}

	teams := []string{}
	if rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeamNames, claims.TenantID); err == nil {
		defer rows.Close()
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				teams = append(teams, n)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "teams": teams})
}

// agentReachable reports whether an agent can take a transferred call now: with
// presence on, online + available; with it off, assume yes.
func (s *Server) agentReachable(ctx context.Context, tenant uuid.UUID, sip string) bool {
	if !s.presence.Enabled() {
		return true
	}
	return s.presence.Available(ctx, tenant.String(), sip)
}

type sdkTransferRequest struct {
	ToSip  string `json:"to_sip"`  // transfer to one agent
	ToTeam string `json:"to_team"` // transfer to a team (ring all available members)
}

// handleSdkTransfer blind-transfers the agent's current call to another agent or
// a team. Every call lives in a media-gateway room, so the gateway runs it
// (POST /control/transfer) exactly as it runs the widget's own /rtc transfer:
// target pre-flight, the agent's leg ends, the target rings, and on no answer
// the caller hears the tenant's message. This used to drive FreeSWITCH over ESL
// on calls.leg_id, which is no FreeSWITCH channel for a media-plane call, so it
// always failed.
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
	req.ToSip, req.ToTeam = strings.TrimSpace(req.ToSip), strings.TrimSpace(req.ToTeam)
	if (req.ToSip == "") == (req.ToTeam == "") {
		writeError(w, http.StatusBadRequest, "exactly one of to_sip or to_team required")
		return
	}

	var roomID string
	err = s.db.Pool.QueryRow(r.Context(), s.q.GetAgentActiveCallRoom, claims.TenantID, agentID).Scan(&roomID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "no active call")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if roomID == "" {
		writeError(w, http.StatusConflict, "call has no media room")
		return
	}

	resp, err := s.gatewayPost(r.Context(), claims.TenantID, "/control/transfer", map[string]string{
		"room": roomID, "sip": claims.SipUsername, "to_sip": req.ToSip, "to_team": req.ToTeam,
	})
	if err != nil {
		s.log.Error("transfer: gateway unreachable", "url", s.gatewayControlURL, "err", err)
		writeError(w, http.StatusBadGateway, "media gateway unavailable")
		return
	}
	defer resp.Body.Close()
	var out struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	switch {
	case resp.StatusCode < 300:
		writeJSON(w, http.StatusOK, map[string]any{"transferred": true})
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusConflict:
		// The gateway's refusals (bad target, agent not on that call, target
		// unavailable, transfer already running); the agent keeps the call.
		writeError(w, resp.StatusCode, out.Error)
	default:
		s.log.Error("transfer: gateway refused", "status", resp.StatusCode, "error", out.Error)
		writeError(w, http.StatusBadGateway, "transfer failed")
	}
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
