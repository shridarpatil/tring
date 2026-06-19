// Package queuekeys defines the Redis keys for published team-queue depth, in a
// neutral place both producers (the FreeSWITCH orchestrator and the pion edge)
// and the consumer (the API/dashboard) can import without depending on each
// other. Depth is published per source and summed at read time, so phone and
// web callers waiting for the same team add up instead of clobbering one value.
package queuekeys

import "time"

// Producers of queue depth. Each writes its OWN source key; the API sums them.
const (
	SourcePhone = "phone" // FreeSWITCH/ESL team queue (internal/orchestrator)
	SourceWeb   = "web"   // pion media plane team queue (internal/media/signaling)
)

// Sources enumerates every producer so the reader can sum across them.
var Sources = []string{SourcePhone, SourceWeb}

// DepthTTL bounds how long a published depth lives without a refresh. Producers
// re-publish on every poll tick while callers wait, so a stale count expires to
// absent (read as 0) within this window of the last caller leaving — and a
// producer crash can't leave a phantom count forever.
const DepthTTL = 10 * time.Second

// DepthKey is the Redis key holding the number of callers a given source has
// waiting in team's queue. tenant is the tenant UUID string.
func DepthKey(tenant, team, source string) string {
	return "tring:queue:depth:" + tenant + ":" + team + ":" + source
}
