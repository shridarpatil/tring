package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---- Team views (/v1/my-teams) ------------------------------------------
//
// The team surface for non-admin console users. Every handler derives which
// teams the caller may see or act on from the Principal (reloaded from the DB
// each request), never from request params: members see their own teams' live
// queue and presence; managers also see today's stats, calls and recordings
// and manage membership of the teams they manage; supervisors and admins do
// all of that for every team and alone toggle the manager flag.

// myTeam is one team as seen by the caller. Stats and Calls are set only for a
// team the caller manages, and only on the single-team endpoint.
type myTeam struct {
	Name      string         `json:"name"`
	IsManager bool           `json:"is_manager"` // the caller's own membership flag
	Queue     queueDepth     `json:"queue"`
	Members   []myTeamMember `json:"members"`
	Stats     *teamStats     `json:"stats,omitempty"`
	Calls     []Call         `json:"calls,omitzero"` // omitzero: an empty day is [], not omitted
}

type myTeamMember struct {
	UserID      uuid.UUID `json:"user_id"`
	SipUsername string    `json:"sip_username"`
	DisplayName string    `json:"display_name,omitempty"`
	IsManager   bool      `json:"is_manager"`
	Status      string    `json:"status"` // agentsList status; "unknown" when presence is unreadable
}

type teamStats struct {
	Offered        int     `json:"offered"`
	Answered       int     `json:"answered"`
	Abandoned      int     `json:"abandoned"`
	AvgWaitSeconds float64 `json:"avg_wait_seconds"`
}

// myTeams builds the live view of the teams include accepts (all of them
// visible to the caller), by name. /v1/my-teams is polled by every agent, so
// presence is read only for members of those teams, never the whole tenant.
func (s *Server) myTeams(r *http.Request, p *Principal, include func(team string) bool) ([]myTeam, error) {
	ctx := r.Context()
	t := tenantFromCtx(ctx)

	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamMemberships, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := map[string][]myTeamMember{}
	shown := map[string]bool{} // sip usernames whose presence we need
	for rows.Next() {
		var team string
		var m myTeamMember
		if err := rows.Scan(&team, &m.UserID, &m.SipUsername, &m.DisplayName, &m.IsManager); err != nil {
			return nil, err
		}
		if !include(team) {
			continue
		}
		members[team] = append(members[team], m)
		shown[m.SipUsername] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// nil agents (load failed) makes staffing read unknown; queues still serve.
	var agents []Agent
	if all, err := s.loadAgents(ctx, t.ID); err != nil {
		s.log.Warn("my-teams: agents", "err", err)
	} else {
		agents = make([]Agent, 0, len(shown))
		for _, a := range all {
			if shown[a.SipUsername] {
				agents = append(agents, a)
			}
		}
		s.fillAgentStatus(ctx, t, agents)
	}
	snap, err := s.queuesSnapshotFor(ctx, t, agents, include)
	if err != nil {
		return nil, err
	}
	status := make(map[string]string, len(agents))
	for _, a := range agents {
		status[a.SipUsername] = a.Status
	}

	out := []myTeam{}
	for _, q := range snap.Queues {
		mt := myTeam{
			Name:      q.Team,
			IsManager: slices.Contains(p.ManagedTeams, q.Team),
			Queue:     q,
			Members:   members[q.Team],
		}
		if mt.Members == nil {
			mt.Members = []myTeamMember{}
		}
		for i := range mt.Members {
			mt.Members[i].Status = status[mt.Members[i].SipUsername]
			if mt.Members[i].Status == "" {
				mt.Members[i].Status = "unknown"
			}
		}
		out = append(out, mt)
	}
	return out, nil
}

func (s *Server) handleListMyTeams(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	teams, err := s.myTeams(r, p, p.CanViewTeam)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, teams)
}

// handleGetMyTeam returns one team. A team the caller can't view is a 404,
// like a missing one, so team names don't leak.
func (s *Server) handleGetMyTeam(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	if !p.CanViewTeam(name) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	teams, err := s.myTeams(r, p, func(team string) bool { return team == name })
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if len(teams) == 0 {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	mt := teams[0]
	if p.ManagesTeam(name) {
		var st teamStats
		if err := s.db.Pool.QueryRow(r.Context(), s.q.TeamStatsToday, p.TenantID, name).
			Scan(&st.Offered, &st.Answered, &st.Abandoned, &st.AvgWaitSeconds); err != nil {
			writeError(w, http.StatusInternalServerError, "stats failed")
			return
		}
		mt.Stats = &st
		if mt.Calls, err = s.teamCallsToday(r, p.TenantID, name); err != nil {
			writeError(w, http.StatusInternalServerError, "calls failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, mt)
}

// teamCallsToday lists today's calls belonging to team, with recording links
// pointing at the team route (the tenant-wide one rejects non-admins).
func (s *Server) teamCallsToday(r *http.Request, tenantID uuid.UUID, team string) ([]Call, error) {
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeamCallsToday, tenantID, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, err
		}
		if c.RecordingURL != nil {
			u := "/v1/my-teams/" + url.PathEscape(team) + "/calls/" + c.ID.String() + "/recording"
			c.RecordingURL = &u
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// handleMyTeamCallRecording serves a recording to a manager of a team the call
// belongs to (calls.team or a queue stint, not the answering agent's teams).
func (s *Server) handleMyTeamCallRecording(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !p.ManagesTeam(name) {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}
	var inTeam bool
	if err := s.db.Pool.QueryRow(r.Context(), s.q.CallInTeams, p.TenantID, id, []string{name}).Scan(&inTeam); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !inTeam {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}
	s.serveCallRecording(w, r, p.TenantID, id)
}

// handleAddMyTeamMember adds an existing tenant user to a team the caller
// manages. Re-adding a member keeps their manager flag (add-team-agent only
// touches position), so this can't grant manager rights.
func (s *Server) handleAddMyTeamMember(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	if !p.ManagesTeam(name) {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}
	if _, ok := s.loadTeam(w, r, p.TenantID, name); !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.AddTeamAgent, p.TenantID, name, userID, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "add failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusBadRequest, "user not found in this tenant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveMyTeamMember removes a member from a team the caller manages.
// A manager can't remove a manager (themselves included): removing and
// re-adding would strip the flag, which only supervisors and admins change.
func (s *Server) handleRemoveMyTeamMember(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	if !p.ManagesTeam(name) {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	// The NOT is_manager guard sits in the DELETE itself, so a concurrent
	// promotion can't slip between a check and the delete.
	q := s.q.RemoveTeamAgentNonManager
	if p.SeesAllTeams() {
		q = s.q.RemoveTeamAgent
	}
	tag, err := s.db.Pool.Exec(r.Context(), q, p.TenantID, name, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "remove failed")
		return
	}
	if tag.RowsAffected() == 0 {
		// Nothing removed: tell "not a member" (404) from "is a manager" (403).
		var isManager bool
		err := s.db.Pool.QueryRow(r.Context(), s.q.GetTeamMemberManager, p.TenantID, name, userID).Scan(&isManager)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "user not in team")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "remove failed")
		default:
			writeError(w, http.StatusForbidden, "only a supervisor can remove a team manager")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetMyTeamManager sets a member's manager flag. Supervisors and admins
// only: a manager can't change it, not even on themselves.
func (s *Server) handleSetMyTeamManager(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	if !p.SeesAllTeams() {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}
	name := chi.URLParam(r, "name")
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		IsManager *bool `json:"is_manager"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IsManager == nil {
		writeError(w, http.StatusBadRequest, "is_manager is required")
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(), s.q.SetTeamAgentManager, p.TenantID, name, userID, *req.IsManager)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not in team")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMyTeamCandidates lists tenant users not yet in the team, for the
// manager's add-member picker (non-admins can't call GET /v1/users).
func (s *Server) handleMyTeamCandidates(w http.ResponseWriter, r *http.Request) {
	p := principalFromCtx(r.Context())
	name := chi.URLParam(r, "name")
	if !p.CanViewTeam(name) {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	if !p.ManagesTeam(name) {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeamCandidates, p.TenantID, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	type candidate struct {
		ID          uuid.UUID `json:"id"`
		DisplayName string    `json:"display_name,omitempty"`
		SipUsername string    `json:"sip_username"`
	}
	out := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.SipUsername); err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
