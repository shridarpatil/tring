package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/queuekeys"
)

// ---- Queues -------------------------------------------------------------
//
// Live state of each team's queue. The orchestrator holds the queues in memory
// and publishes each team's depth to Redis; the API (a separate process) reads
// it back here for the dashboard. Best-effort: with no Redis, every team's
// depth reads 0. Staffing, open hours, waits and SLA breaches join it.

type queueDepth struct {
	Team    string `json:"team"`
	Waiting int    `json:"waiting"`

	// Live team state for the dashboard. Open/MinLive/SLASeconds come from the
	// team's settings; the staffing counts from agent presence; WaitingSince and
	// BreachesToday from queue_stints. WaitingSince holds timestamps, not ages,
	// so an unchanged queue renders identical bytes (see events.Hub.Every).
	Open          bool            `json:"open"`
	MinLive       int             `json:"min_live_agents"`
	SLASeconds    int             `json:"sla_seconds"`
	Members       int             `json:"members"`
	Agents        []string        `json:"agents"` // member SIP usernames, sorted, never null
	Live          int             `json:"live"`   // on_call + available
	OnCall        int             `json:"on_call"`
	Available     int             `json:"available"`
	Away          int             `json:"away"`
	WaitingSince  []time.Time     `json:"waiting_since"` // open stints, oldest first, never null
	BreachesToday int             `json:"breaches_today"`
	BelowMinSince *time.Time      `json:"below_min_since,omitempty"`
	Hours         json.RawMessage `json:"hours"` // the team's working-hours schedule, [] when always open
}

type queuesResponse struct {
	Total  int          `json:"total"`   // callers waiting across all team queues
	OnCall int          `json:"on_call"` // live calls currently connected to an agent
	Queues []queueDepth `json:"queues"`
}

// queuesSnapshot builds each team's live state: per-source (phone+web) queue
// depth from Redis, staffing from agents (agentsList output, passed in because
// the dashboard snapshot already has it), and waits/breaches from queue_stints.
// The endpoint and the SSE recompute share it.
func (s *Server) queuesSnapshot(ctx context.Context, t *Tenant, agents []Agent) (queuesResponse, error) {
	now := time.Now()
	var onCall int
	_ = s.db.Pool.QueryRow(ctx, s.q.CountActiveAgentCalls, t.ID).Scan(&onCall)

	out := queuesResponse{OnCall: onCall, Queues: []queueDepth{}}
	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamLiveConfig, t.ID)
	if err != nil {
		return queuesResponse{}, err
	}
	defer rows.Close()
	hours := map[string]json.RawMessage{}
	for rows.Next() {
		var q queueDepth
		var h json.RawMessage
		if err := rows.Scan(&q.Team, &q.MinLive, &q.SLASeconds, &h); err != nil {
			return queuesResponse{}, err
		}
		q.WaitingSince = []time.Time{}
		q.Agents = []string{}
		q.Hours = h
		if len(bytes.TrimSpace(h)) == 0 || string(h) == "null" {
			q.Hours = json.RawMessage("[]")
		}
		hours[q.Team] = h
		out.Queues = append(out.Queues, q)
	}
	if err := rows.Err(); err != nil {
		return queuesResponse{}, err
	}

	staff, members, err := s.teamStaff(ctx, t.ID, agents)
	if err != nil {
		return queuesResponse{}, err
	}
	waiting, err := s.openStints(ctx, t.ID)
	if err != nil {
		return queuesResponse{}, err
	}
	breaches, err := s.breachesToday(ctx, t.ID)
	if err != nil {
		return queuesResponse{}, err
	}

	for i := range out.Queues {
		q := &out.Queues[i]
		for _, src := range queuekeys.Sources {
			if v, ok := s.cache.Get(ctx, queuekeys.DepthKey(t.ID.String(), q.Team, src)); ok {
				x, _ := strconv.Atoi(string(v))
				q.Waiting += x
			}
		}
		out.Total += q.Waiting

		c := staff[q.Team]
		q.Members, q.Live, q.OnCall, q.Available, q.Away = c.Members, c.Live, c.OnCall, c.Available, c.Away
		if m := members[q.Team]; m != nil {
			q.Agents = m
		}
		if w := waiting[q.Team]; w != nil {
			q.WaitingSince = w
		}
		q.BreachesToday = breaches[q.Team]
		q.Open = ivr.ScheduleOpen(hours[q.Team], now)
		below := q.Open && q.MinLive > 0 && q.Live < q.MinLive
		q.BelowMinSince = s.belowMin.observe(t.ID.String()+"|"+q.Team, below, now)
	}
	return out, nil
}

// teamStaff counts each team's members by their agentsList status, and lists
// each team's member SIP usernames (sorted) for the dashboard's agent board.
func (s *Server) teamStaff(ctx context.Context, tenantID uuid.UUID, agents []Agent) (map[string]staffCounts, map[string][]string, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamAgentStatus, tenantID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var members [][2]string
	for rows.Next() {
		var m [2]string
		if err := rows.Scan(&m[0], &m[1]); err != nil {
			return nil, nil, err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sips := map[string][]string{}
	for _, m := range members {
		sips[m[0]] = append(sips[m[0]], m[1])
	}
	for _, l := range sips {
		sort.Strings(l)
	}
	status := make(map[string]string, len(agents))
	for _, a := range agents {
		status[a.SipUsername] = a.Status
	}
	return teamStaffing(members, status), sips, nil
}

// openStints returns each team's waiting callers' enqueue times, oldest first.
func (s *Server) openStints(ctx context.Context, tenantID uuid.UUID) (map[string][]time.Time, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.ListOpenStints, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]time.Time{}
	for rows.Next() {
		var team string
		var at time.Time
		if err := rows.Scan(&team, &at); err != nil {
			return nil, err
		}
		out[team] = append(out[team], at)
	}
	return out, rows.Err()
}

// breachesToday returns each team's SLA breaches since local midnight.
func (s *Server) breachesToday(ctx context.Context, tenantID uuid.UUID) (map[string]int, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.CountBreachesTodayByTeam, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var team string
		var n int
		if err := rows.Scan(&team, &n); err != nil {
			return nil, err
		}
		out[team] = n
	}
	return out, rows.Err()
}

func (s *Server) handleListQueues(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	agents, err := s.agentsList(r.Context(), t)
	if err != nil {
		s.log.Warn("queues: agents", "err", err) // staffing reads 0; depths still serve
	}
	out, err := s.queuesSnapshot(r.Context(), t, agents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
