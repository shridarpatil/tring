package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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

// myTeams builds the live view of every team the caller can see, by name.
func (s *Server) myTeams(r *http.Request, p *Principal) ([]myTeam, error) {
	ctx := r.Context()
	t := tenantFromCtx(ctx)
	agents, err := s.agentsList(ctx, t)
	if err != nil {
		s.log.Warn("my-teams: agents", "err", err) // statuses read unknown; queues still serve
	}
	snap, err := s.queuesSnapshot(ctx, t, agents)
	if err != nil {
		return nil, err
	}
	status := make(map[string]string, len(agents))
	for _, a := range agents {
		status[a.SipUsername] = a.Status
	}

	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamMemberships, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := map[string][]myTeamMember{}
	for rows.Next() {
		var team string
		var m myTeamMember
		if err := rows.Scan(&team, &m.UserID, &m.SipUsername, &m.DisplayName, &m.IsManager); err != nil {
			return nil, err
		}
		m.Status = status[m.SipUsername]
		if m.Status == "" {
			m.Status = "unknown"
		}
		members[team] = append(members[team], m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []myTeam{}
	for _, q := range snap.Queues {
		if !p.CanViewTeam(q.Team) {
			continue
		}
		mt := myTeam{
			Name:      q.Team,
			IsManager: slices.Contains(p.ManagedTeams, q.Team),
			Queue:     q,
			Members:   members[q.Team],
		}
		if mt.Members == nil {
			mt.Members = []myTeamMember{}
		}
		out = append(out, mt)
	}
	return out, nil
}

func (s *Server) handleListMyTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := s.myTeams(r, principalFromCtx(r.Context()))
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
	teams, err := s.myTeams(r, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	i := slices.IndexFunc(teams, func(t myTeam) bool { return t.Name == name })
	if i < 0 {
		writeError(w, http.StatusNotFound, "team not found")
		return
	}
	mt := teams[i]
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
	tag, err := s.db.Pool.Exec(r.Context(), s.q.RemoveTeamAgent, p.TenantID, name, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "remove failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not in team")
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
