package api

import (
	"context"
	"math"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/callrow"
)

// meDashboard is dashboardSnapshot with optional stats: the console polls the
// live panels every few seconds but the day's stats far less often.
type meDashboard struct {
	Calls  []Call         `json:"calls"`
	Agents []Agent        `json:"agents"`
	Queues queuesResponse `json:"queues"`
	Stats  *statsOverview `json:"stats,omitempty"`
}

// handleMeDashboard is the dashboard for a non-admin console user: the same
// shape as the stream's snapshot event, scoped to what the caller may see.
// ?stats=0 skips the stats queries (and the field). Supervisors (and admins)
// get the full tenant-wide snapshot, so a client can use this one endpoint for
// every role; the console uses the live stream for them.
func (s *Server) handleMeDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFromCtx(ctx)
	t := tenantFromCtx(ctx)
	if p.SeesAllTeams() {
		writeJSON(w, http.StatusOK, s.buildDashboardSnapshot(ctx, t))
		return
	}
	snap, err := s.scopedDashboardSnapshot(ctx, t, p, r.URL.Query().Get("stats") != "0")
	if err != nil {
		s.log.Error("me dashboard failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// scopedDashboardSnapshot builds an agent's or team manager's dashboard. A
// manager sees the teams they manage; a plain agent the teams they belong to.
// Live calls are the caller's own plus, for a manager, their managed teams'
// calls: a plain agent never receives another caller's number. Trend stats are
// the caller's own calls; queue figures cover the included teams.
func (s *Server) scopedDashboardSnapshot(ctx context.Context, t *Tenant, p *Principal, withStats bool) (meDashboard, error) {
	teams := p.Teams
	if len(p.ManagedTeams) > 0 {
		teams = p.ManagedTeams
	}
	if teams == nil {
		teams = []string{}
	}
	callTeams := p.ManagedTeams
	if callTeams == nil {
		callTeams = []string{} // a NULL array would match nothing too; be explicit
	}
	include := func(team string) bool { return slices.Contains(teams, team) }

	snap := meDashboard{}
	var err error
	if snap.Calls, err = s.scopedActiveCalls(ctx, t.ID, p.UserID, callTeams); err != nil {
		return snap, err
	}
	if snap.Agents, err = s.teamMembersAgents(ctx, t, include); err != nil {
		return snap, err
	}
	if snap.Queues, err = s.queuesSnapshotFor(ctx, t, snap.Agents, include); err != nil {
		return snap, err
	}
	// on_call counts only the calls shown (queuesSnapshotFor leaves it 0).
	for _, c := range snap.Calls {
		if c.Status == "answered" {
			snap.Queues.OnCall++
		}
	}
	if withStats {
		st, err := s.personalStats(ctx, t.ID, p.UserID, teams)
		if err != nil {
			return snap, err
		}
		snap.Stats = &st
	}
	return snap, nil
}

func (s *Server) scopedActiveCalls(ctx context.Context, tenantID, userID uuid.UUID, teams []string) ([]Call, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListActiveCallsScoped, tenantID, userID, teams)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		c, err := callrow.ScanActive(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// teamMembersAgents lists the members of the teams include accepts, with
// presence read only for them.
func (s *Server) teamMembersAgents(ctx context.Context, t *Tenant, include func(team string) bool) ([]Agent, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamMemberships, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := map[uuid.UUID]bool{}
	for rows.Next() {
		var team, sip, name string
		var id uuid.UUID
		var isManager bool
		if err := rows.Scan(&team, &id, &sip, &name, &isManager); err != nil {
			return nil, err
		}
		if include(team) {
			members[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	all, err := s.loadAgents(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, 0, len(members))
	for _, a := range all {
		if members[a.ID] {
			agents = append(agents, a)
		}
	}
	s.fillAgentStatus(ctx, t, agents)
	return agents, nil
}

// personalStats is statsOverview for one user's calls, with the queue figures
// (SLA breaches, average queue wait) over teams. Nothing a single agent owns
// fails before connecting, so FailedToday stays 0.
func (s *Server) personalStats(ctx context.Context, tenantID, userID uuid.UUID, teams []string) (statsOverview, error) {
	var total, took, missed int
	var avgWait, avgTalk, avgQueue float64
	if err := s.db.Pool.QueryRow(ctx, s.q.StatsMeToday, tenantID, userID).
		Scan(&total, &took, &missed, &avgWait, &avgTalk); err != nil {
		return statsOverview{}, err
	}
	out := statsOverview{
		CallsToday:     total,
		AbandonedToday: missed,
		AvgWaitSeconds: int(math.Round(avgWait)),
		AvgTalkSeconds: int(math.Round(avgTalk)),
		Hourly:         []statsHourBucket{},
	}
	if total > 0 {
		out.AnsweredPct = int(math.Round(float64(took) / float64(total) * 100))
	}
	if err := s.db.Pool.QueryRow(ctx, s.q.StatsQueueTodayTeams, tenantID, teams).
		Scan(&out.SLABreachesToday, &avgQueue); err != nil {
		return statsOverview{}, err
	}
	out.AvgQueueWaitSeconds = int(math.Round(avgQueue))
	rows, err := s.db.Pool.Query(ctx, s.q.StatsMeHourly, tenantID, userID, teams)
	if err != nil {
		return statsOverview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var b statsHourBucket
		if err := rows.Scan(&b.Hour, &b.Count, &b.Breaches); err != nil {
			return statsOverview{}, err
		}
		out.Hourly = append(out.Hourly, b)
	}
	return out, rows.Err()
}
