package api

import (
	"net/http"
	"strconv"

	"github.com/whatomate/tring/internal/queuekeys"
)

// ---- Queues -------------------------------------------------------------
//
// Live count of callers waiting in each team's queue. The orchestrator holds
// the queues in memory and publishes each team's depth to Redis; the API (a
// separate process) reads it back here for the dashboard. Best-effort: with no
// Redis, every team reads 0.

type queueDepth struct {
	Team    string `json:"team"`
	Waiting int    `json:"waiting"`
}

type queuesResponse struct {
	Total  int          `json:"total"`   // callers waiting across all team queues
	OnCall int          `json:"on_call"` // live calls currently connected to an agent
	Queues []queueDepth `json:"queues"`
}

func (s *Server) handleListQueues(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())

	var onCall int
	_ = s.db.Pool.QueryRow(r.Context(), s.q.CountActiveAgentCalls, t.ID).Scan(&onCall)

	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeamNames, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		names = append(names, name)
	}

	out := queuesResponse{OnCall: onCall, Queues: []queueDepth{}}
	for _, name := range names {
		// Sum every producer's published depth for this team (phone + web) so a
		// caller waiting on either media plane is counted once. Each source key
		// self-heals via TTL, so a stopped producer drops out within DepthTTL.
		n := 0
		for _, src := range queuekeys.Sources {
			if v, ok := s.cache.Get(r.Context(), queuekeys.DepthKey(t.ID.String(), name, src)); ok {
				x, _ := strconv.Atoi(string(v))
				n += x
			}
		}
		out.Total += n
		out.Queues = append(out.Queues, queueDepth{Team: name, Waiting: n})
	}
	writeJSON(w, http.StatusOK, out)
}
