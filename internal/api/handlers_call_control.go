package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/agentleg"
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
			var id, tid uuid.UUID
			var sip, name string
			var avail bool
			var created time.Time
			if err := rows.Scan(&id, &tid, &sip, &name, &avail, &created); err != nil {
				continue
			}
			if id == selfID || !avail || !s.agentReachable(r.Context(), claims.TenantID, sip) {
				continue
			}
			if name == "" {
				name = sip
			}
			agents = append(agents, agentTarget{Sip: sip, Name: name})
		}
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
// a team: the client is re-bridged to the target (hearing the tenant's hold
// music as ringback while it rings) and the requesting agent's leg drops. A
// re-bridge — NOT uuid_hold — avoids a SIP re-INVITE, so the media-gateway legs
// survive. The bridge runs in the tring_transfer dialplan extension, fed the
// dial-string via a channel var (so a ring-all team's commas don't break it).
// Blind has no fallback, so the target must be reachable. Attended transfer and
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

	// Resolve the target into a FreeSWITCH bridge dial-string.
	var dest string
	switch {
	case strings.TrimSpace(req.ToTeam) != "":
		dest = s.teamDialString(r.Context(), claims.TenantID, strings.TrimSpace(req.ToTeam))
		if dest == "" {
			writeError(w, http.StatusConflict, "no available members in that team")
			return
		}
	case strings.TrimSpace(req.ToSip) != "":
		sip := strings.TrimSpace(req.ToSip)
		var targetID uuid.UUID
		var label string
		if err := s.db.Pool.QueryRow(r.Context(), s.q.GetAgentBySIP, claims.TenantID, sip).Scan(&targetID, &label); err != nil {
			writeError(w, http.StatusNotFound, "unknown target agent")
			return
		}
		if targetID == agentID {
			writeError(w, http.StatusBadRequest, "cannot transfer to yourself")
			return
		}
		if !s.agentReachable(r.Context(), claims.TenantID, sip) {
			writeError(w, http.StatusConflict, "target agent is not available")
			return
		}
		dest = agentleg.Build(agentleg.Options{
			Transport: "gateway", Gateway: s.agentGateway, SIP: sip,
			TenantID: claims.TenantID.String(), Attribute: true,
		})
	default:
		writeError(w, http.StatusBadRequest, "to_sip or to_team required")
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

	// Feed the bridge target + ringback to the tring_transfer dialplan extension,
	// then re-route the client there (the original agent's leg ends).
	if _, err := s.fs.Command(r.Context(), "uuid_setvar", leg+" transfer_target "+dest); err != nil {
		s.log.Error("transfer setvar failed", "err", err)
		writeError(w, http.StatusBadGateway, "transfer failed")
		return
	}
	_, _ = s.fs.Command(r.Context(), "uuid_setvar", leg+" transfer_ringback "+moh)
	// Mark the leg so the orchestrator's IVR executor knows this bridge ended
	// because of a blind transfer (caller handed to a new bridge), not a normal
	// hangup — so it doesn't walk on to a hangup node or post-walk HangupCall and
	// drop the transferred caller. Read back via wasBlindTransferred.
	_, _ = s.fs.Command(r.Context(), "uuid_setvar", leg+" tring_blind_transfer true")
	if _, err := s.fs.Command(r.Context(), "uuid_transfer", leg+" tring_transfer XML default"); err != nil {
		s.log.Error("transfer failed", "agent", agentID, "err", err)
		writeError(w, http.StatusBadGateway, "transfer command failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transferred": true})
}

// teamDialString builds a ring-all bridge dial-string for a team's reachable
// members (comma-joined = all ring at once), or "" if none are available.
func (s *Server) teamDialString(ctx context.Context, tenant uuid.UUID, team string) string {
	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamAgentSips, tenant, team)
	if err != nil {
		return ""
	}
	defer rows.Close()
	legs := []string{}
	for rows.Next() {
		var sip string
		var pos int
		if rows.Scan(&sip, &pos) != nil {
			continue
		}
		if !s.agentReachable(ctx, tenant, sip) {
			continue
		}
		legs = append(legs, agentleg.Build(agentleg.Options{
			Transport: "gateway", Gateway: s.agentGateway, SIP: sip,
			TenantID: tenant.String(), Attribute: true,
		}))
	}
	return strings.Join(legs, ",")
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
