package signaling

import (
	"strings"

	"github.com/shridarpatil/tring/internal/cluster"
)

// ClusterSnapshot is this node's full contribution to the cluster state, taken
// right before each heartbeat. Rewriting it whole each time is what repairs any
// immediate Set* write that failed or landed out of order.
func (s *Server) ClusterSnapshot() cluster.Snapshot {
	// Outside s.mu: the room manager has its own lock.
	legs := s.activeCalls()

	s.mu.Lock()
	defer s.mu.Unlock()
	snap := cluster.Snapshot{
		Legs:     legs,
		Draining: s.draining.Load(),
		Online:   make(map[string][]string, len(s.agents)),
		Away:     make(map[string][]string, len(s.agents)),
		Counts: map[cluster.Kind]map[string]int{
			cluster.OnCall: countsOf(s.onCall),
			cluster.Dials:  countsOf(s.agentDials),
		},
	}
	for tenant, socks := range s.agents {
		// Empty slices too, so the heartbeat clears a tenant's sets the moment
		// its last agent leaves instead of waiting for them to expire.
		online, away := make([]string, 0, len(socks)), []string{}
		for sip := range socks {
			online = append(online, sip)
		}
		snap.Online[tenant], snap.Away[tenant] = online, away
	}
	for k, on := range s.away {
		tenant, sip, ok := strings.Cut(k, "|")
		if !on || !ok || s.agents[tenant][sip] == nil {
			continue
		}
		snap.Away[tenant] = append(snap.Away[tenant], sip)
	}
	return snap
}

// countsOf copies the positive entries of a "tenant|sip" → n map.
func countsOf(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		if n > 0 {
			out[k] = n
		}
	}
	return out
}
