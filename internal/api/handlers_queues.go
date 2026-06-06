package api

import (
	"net/http"
	"strconv"

	"github.com/whatomate/tring/internal/orchestrator"
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
	Total  int          `json:"total"`
	Queues []queueDepth `json:"queues"`
}

func (s *Server) handleListQueues(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), s.q.ListTeams, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var tm Team
		if err := rows.Scan(&tm.TenantID, &tm.Name, &tm.Strategy, &tm.RingSeconds, &tm.MaxWaitSeconds, &tm.MohSound, &tm.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		names = append(names, tm.Name)
	}

	out := queuesResponse{Queues: []queueDepth{}}
	for _, name := range names {
		n := 0
		if v, ok := s.cache.Get(r.Context(), orchestrator.QueueDepthKey(t.ID, name)); ok {
			n, _ = strconv.Atoi(string(v))
		}
		out.Total += n
		out.Queues = append(out.Queues, queueDepth{Team: name, Waiting: n})
	}
	writeJSON(w, http.StatusOK, out)
}
