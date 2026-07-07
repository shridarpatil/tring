// Package trunkselect is the shared outbound-trunk selection discipline used by
// both the FreeSWITCH ESL path (internal/api) and the pion plane (the gateway's
// fsplug Originator). The strategy logic lives here once. Input trunks are
// assumed already priority-ordered (lowest first); this package does not sort.
package trunkselect

import "fmt"

type Strategy string

const (
	Failover   Strategy = "failover"
	RoundRobin Strategy = "round_robin"
	Manual     Strategy = "manual"
)

// Trunk is one active outbound DID (caller-ID E164) + its FS gateway name.
// A neutral shape callers map their own trunk rows into.
type Trunk struct {
	E164 string
	Name string // FreeSWITCH gateway name (e.g. from freeswitch.GatewayName)
}

// SelectIndices is the pure core: given n priority-ordered trunks and the
// strategy, it returns which indices to use. rrIndex is the caller-supplied
// round-robin index (each caller owns its own RR state, e.g. per-tenant); it is
// only consulted for RoundRobin. Empty (n==0) returns (nil,nil) so the caller
// decides (the API falls back to loopback; the gateway errors).
func SelectIndices(n int, strategy Strategy, rrIndex int) ([]int, error) {
	if n == 0 {
		return nil, nil
	}
	switch strategy {
	case "", Failover:
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx, nil
	case Manual:
		return []int{0}, nil
	case RoundRobin:
		i := rrIndex % n
		if i < 0 {
			i += n
		}
		return []int{i}, nil
	case "load_based":
		return nil, fmt.Errorf("load_based strategy is reserved and not yet implemented")
	default:
		return nil, fmt.Errorf("unknown outbound_strategy %q", strategy)
	}
}

// Select is a convenience over SelectIndices for callers holding a []Trunk.
func Select(ordered []Trunk, strategy Strategy, rrIndex int) ([]Trunk, error) {
	idx, err := SelectIndices(len(ordered), strategy, rrIndex)
	if err != nil {
		return nil, err
	}
	out := make([]Trunk, len(idx))
	for i, x := range idx {
		out[i] = ordered[x]
	}
	return out, nil
}
