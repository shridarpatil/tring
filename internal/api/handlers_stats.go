package api

import (
	"math"
	"net/http"
	"time"
)

// ---- Dashboard stats ----------------------------------------------------
//
// "Today so far" aggregates for the Live Ops cockpit trend strip: running daily
// totals (calls / answered % / avg talk) plus a 12-hour calls-per-hour series.
// Read-only, tenant-scoped; cheap enough to poll alongside the other panels.

type statsHourBucket struct {
	Hour  time.Time `json:"hour"`  // start of the hour, in the DB session timezone
	Count int       `json:"count"`
}

type statsOverview struct {
	CallsToday     int               `json:"calls_today"`
	AnsweredPct    int               `json:"answered_pct"`     // 0–100; 0 when no calls today
	AbandonedToday int               `json:"abandoned_today"`  // callers who hung up while waiting
	FailedToday    int               `json:"failed_today"`     // calls that errored before connecting
	AvgWaitSeconds int               `json:"avg_wait_seconds"` // time-to-answer (start→connect) of answered calls
	AvgTalkSeconds int               `json:"avg_talk_seconds"` // average completed-call duration
	Hourly         []statsHourBucket `json:"hourly"`           // always 12 buckets, oldest→newest
}

func (s *Server) handleStatsOverview(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())

	var (
		total     int
		answered  int
		abandoned int
		failed    int
		avgWait   float64
		avgTalk   float64
	)
	if err := s.db.Pool.QueryRow(r.Context(), s.q.StatsToday, t.ID).Scan(&total, &answered, &abandoned, &failed, &avgWait, &avgTalk); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	out := statsOverview{
		CallsToday:     total,
		AbandonedToday: abandoned,
		FailedToday:    failed,
		AvgWaitSeconds: int(math.Round(avgWait)),
		AvgTalkSeconds: int(math.Round(avgTalk)),
		Hourly:         []statsHourBucket{},
	}
	if total > 0 {
		out.AnsweredPct = int(math.Round(float64(answered) / float64(total) * 100))
	}

	// The query already returns exactly 12 gap-filled buckets, bucketed in the DB
	// session timezone — no timezone math here, so nothing can mismatch.
	rows, err := s.db.Pool.Query(r.Context(), s.q.StatsHourly, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var b statsHourBucket
		if err := rows.Scan(&b.Hour, &b.Count); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out.Hourly = append(out.Hourly, b)
	}

	writeJSON(w, http.StatusOK, out)
}
