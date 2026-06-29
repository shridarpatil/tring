package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/queuekeys"
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

// queuesSnapshot sums per-source (phone+web) queue depth per team from Redis. The
// endpoint and the SSE recompute share it.
func (s *Server) queuesSnapshot(ctx context.Context, tenantID uuid.UUID) (queuesResponse, error) {
	var onCall int
	_ = s.db.Pool.QueryRow(ctx, s.q.CountActiveAgentCalls, tenantID).Scan(&onCall)

	rows, err := s.db.Pool.Query(ctx, s.q.ListTeamNames, tenantID)
	if err != nil {
		return queuesResponse{}, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return queuesResponse{}, err
		}
		names = append(names, name)
	}

	out := queuesResponse{OnCall: onCall, Queues: []queueDepth{}}
	for _, name := range names {
		n := 0
		for _, src := range queuekeys.Sources {
			if v, ok := s.cache.Get(ctx, queuekeys.DepthKey(tenantID.String(), name, src)); ok {
				x, _ := strconv.Atoi(string(v))
				n += x
			}
		}
		out.Total += n
		out.Queues = append(out.Queues, queueDepth{Team: name, Waiting: n})
	}
	return out, nil
}

func (s *Server) handleListQueues(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	out, err := s.queuesSnapshot(r.Context(), t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
