package routing

import (
	"context"

	"github.com/whatomate/tring/internal/presence"
)

// FilterAvailable keeps only agents that are present-available (online, not
// on_call) AND not currently reserved (being rung by another caller). Reading
// the SHARED presence store is what makes routing cross-plane: a phone call on
// the FreeSWITCH side marks an agent on_call, and the pion side filters it out
// here (and vice-versa). When presence is disabled it returns the input
// unchanged — the caller decides whether filtering applies at all (e.g. the ESL
// plane skips it for the legacy direct-SIP transport, where FreeSWITCH fails
// dead legs itself). A nil reservation store skips the reservation check.
func FilterAvailable(ctx context.Context, pres *presence.Store, res Reservations, tenantID string, sips []string) []string {
	if pres == nil || !pres.Enabled() {
		return sips
	}
	// Fresh slice — do NOT reuse sips' backing array, which would corrupt the
	// caller's member list across queue-poll iterations.
	avail := make([]string, 0, len(sips))
	for _, sip := range sips {
		if res != nil && res.Reserved(ctx, ResKey(tenantID, sip)) {
			continue
		}
		if pres.Available(ctx, tenantID, sip) {
			avail = append(avail, sip)
		}
	}
	return avail
}
