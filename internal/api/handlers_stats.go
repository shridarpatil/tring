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
	Hour  time.Time `json:"hour"`  // start of the hour (UTC); the client renders the local label
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

const statsHourlyWindow = 12

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
		Hourly:         hourlyBuckets(nil, time.Now()),
	}
	if total > 0 {
		out.AnsweredPct = int(math.Round(float64(answered) / float64(total) * 100))
	}

	rows, err := s.db.Pool.Query(r.Context(), s.q.StatsHourly, t.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var hour time.Time
		var n int
		if err := rows.Scan(&hour, &n); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		counts[hour.Unix()] = n
	}
	out.Hourly = hourlyBuckets(counts, time.Now())

	writeJSON(w, http.StatusOK, out)
}

// hourlyBuckets returns a fixed-length series of the last statsHourlyWindow hours
// ending at now's hour, filling absent hours with 0 so the chart's x-axis is
// stable. counts maps an hour's Unix start to its call count (nil ⇒ all zero).
// Pure (clock injected) so it's unit-testable without a DB.
func hourlyBuckets(counts map[int64]int, now time.Time) []statsHourBucket {
	cur := now.UTC().Truncate(time.Hour)
	out := make([]statsHourBucket, statsHourlyWindow)
	for i := range statsHourlyWindow {
		h := cur.Add(time.Duration(i-(statsHourlyWindow-1)) * time.Hour)
		out[i] = statsHourBucket{Hour: h, Count: counts[h.Unix()]}
	}
	return out
}
