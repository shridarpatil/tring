package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shridarpatil/tring/internal/flowstore"
)

// ---- Teams --------------------------------------------------------------
//
// A team is a tenant-scoped group of agents the IVR transfer node rings as a
// unit, with a strategy and queue settings. It is identified by NAME (no
// surrogate id): the name is what a flow carries to target the team, so the
// path param is the team name. See internal/routing (Router.Run) for
// the consumer (strategy-ordered ringing + queue overflow).

// teamStrategies is the allowed strategy set, validated in Go (the
// tenants.outbound_strategy precedent — no Postgres enum).
var teamStrategies = map[string]bool{
	"ring_all":     true,
	"round_robin":  true,
	"sequential":   true,
	"longest_wait": true,
}

type Team struct {
	TenantID       uuid.UUID `json:"tenant_id"`
	Name           string    `json:"name"`
	Strategy       string    `json:"strategy"`
	RingSeconds    int       `json:"ring_seconds"`
	MaxWaitSeconds int       `json:"max_wait_seconds"`
	MohSound       string    `json:"moh_sound"`
	CreatedAt      time.Time `json:"created_at"`
	// Members is populated only on the single-team GET, not the list.
	Members []TeamMember `json:"members,omitempty"`
}

type TeamMember struct {
	AgentID     uuid.UUID `json:"agent_id"`
	SipUsername string    `json:"sip_username"`
	DisplayName string    `json:"display_name,omitempty"`
	Available   bool      `json:"available_for_calls"`
	Position    int       `json:"position"`
}

type createTeamRequest struct {
	Name           string `json:"name"`
	Strategy       string `json:"strategy"`
	RingSeconds    int    `json:"ring_seconds"`
	MaxWaitSeconds int    `json:"max_wait_seconds"`
	MohSound       string `json:"moh_sound"`
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req createTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	// Apply defaults, then validate the (possibly defaulted) strategy.
	if req.Strategy == "" {
		req.Strategy = "ring_all"
	}
	if !teamStrategies[req.Strategy] {
		writeError(w, http.StatusBadRequest, "strategy must be ring_all|round_robin|sequential|longest_wait")
		return
	}
	if req.RingSeconds <= 0 {
		req.RingSeconds = 20
	}
	if req.MaxWaitSeconds <= 0 {
		req.MaxWaitSeconds = 120
	}
	if req.MohSound == "" {
		req.MohSound = "local_stream://moh"
	}

	var tm Team
	err := s.db.Pool.QueryRow(r.Context(), s.q.CreateTeam,
		t.ID, req.Name, req.Strategy, req.RingSeconds, req.MaxWaitSeconds, req.MohSound).Scan(
		&tm.TenantID, &tm.Name, &tm.Strategy, &tm.RingSeconds, &tm.MaxWaitSeconds, &tm.MohSound, &tm.CreatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "a team with this name already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	writeJSON(w, http.StatusCreated, tm)
}

func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeams, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	teams := []Team{}
	for rows.Next() {
		var tm Team
		if err := rows.Scan(&tm.TenantID, &tm.Name, &tm.Strategy, &tm.RingSeconds, &tm.MaxWaitSeconds, &tm.MohSound, &tm.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		teams = append(teams, tm)
	}
	writeJSON(w, http.StatusOK, teams)
}

func (s *Server) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	tm, ok := s.loadTeam(w, r, t.ID, name)
	if !ok {
		return
	}
	members, err := s.listTeamMembers(r, t.ID, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "member lookup failed")
		return
	}
	tm.Members = members
	writeJSON(w, http.StatusOK, tm)
}

type patchTeamRequest struct {
	Strategy       *string `json:"strategy"`
	RingSeconds    *int    `json:"ring_seconds"`
	MaxWaitSeconds *int    `json:"max_wait_seconds"`
	MohSound       *string `json:"moh_sound"`
}

func (s *Server) handlePatchTeam(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	var req patchTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Strategy != nil && !teamStrategies[*req.Strategy] {
		writeError(w, http.StatusBadRequest, "strategy must be ring_all|round_robin|sequential|longest_wait")
		return
	}
	var tm Team
	err := s.db.Pool.QueryRow(r.Context(), s.q.UpdateTeam,
		t.ID, name, req.Strategy, req.RingSeconds, req.MaxWaitSeconds, req.MohSound).Scan(
		&tm.TenantID, &tm.Name, &tm.Strategy, &tm.RingSeconds, &tm.MaxWaitSeconds, &tm.MohSound, &tm.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	s.invalidateTeam(r.Context(), t.ID, name)
	writeJSON(w, http.StatusOK, tm)
}

// invalidateTeam drops the gateway's cached queue settings for a team so an
// edit takes effect on the next transfer instead of after the TTL. Best-effort.
func (s *Server) invalidateTeam(ctx context.Context, tenantID uuid.UUID, team string) {
	if err := s.cache.Delete(ctx, flowstore.TeamCacheKey(tenantID, team)); err != nil {
		s.log.Warn("team cache invalidate failed", "team", team, "err", err)
	}
}

func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	tag, err := s.db.Pool.Exec(r.Context(), s.q.DeleteTeam, t.ID, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	s.invalidateTeam(r.Context(), t.ID, name)
	w.WriteHeader(http.StatusNoContent)
}

type addTeamAgentRequest struct {
	AgentID  string `json:"agent_id"`
	Position int    `json:"position"`
}

// handleAddTeamAgent adds (or repositions) an agent in a team. Returns the
// refreshed member list. 404 if the team doesn't exist; 400 if the agent id
// is malformed or not in this tenant.
func (s *Server) handleAddTeamAgent(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	if _, ok := s.loadTeam(w, r, t.ID, name); !ok {
		return
	}
	var req addTeamAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	agentID, err := uuid.Parse(req.AgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent_id")
		return
	}
	if req.Position <= 0 {
		req.Position = 1
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.AddTeamAgent, t.ID, name, agentID, req.Position)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "add failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusBadRequest, "agent not found in this tenant")
		return
	}
	members, err := s.listTeamMembers(r, t.ID, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "member lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) handleRemoveTeamAgent(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	agentID, err := uuid.Parse(chi.URLParam(r, "agentID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.RemoveTeamAgent, t.ID, name, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "remove failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "agent not in team")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ------------------------------------------------------------

// loadTeam fetches a team by name, writing a 404/500 and returning ok=false on
// miss/error so callers can `if _, ok := s.loadTeam(...); !ok { return }`.
func (s *Server) loadTeam(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, name string) (Team, bool) {
	var tm Team
	err := s.db.Pool.QueryRow(r.Context(), s.q.GetTeam, tenantID, name).Scan(
		&tm.TenantID, &tm.Name, &tm.Strategy, &tm.RingSeconds, &tm.MaxWaitSeconds, &tm.MohSound, &tm.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team not found")
		return Team{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return Team{}, false
	}
	return tm, true
}

func (s *Server) listTeamMembers(r *http.Request, tenantID uuid.UUID, name string) ([]TeamMember, error) {
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeamAgents, tenantID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []TeamMember{}
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.AgentID, &m.SipUsername, &m.DisplayName, &m.Available, &m.Position); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), used to map a duplicate insert to 409.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
