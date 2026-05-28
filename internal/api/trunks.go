package api

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/freeswitch"
)

// trunk is the bare info originate needs about an outbound number — the
// allocated DID, the FreeSWITCH gateway to dial through, and the
// provider_type used for E.164 normalization. Numbers must reference a
// trunk via numbers.trunk_id; numbers without it are skipped (operator
// must attach one via the UI).
type trunk struct {
	E164         string
	Gateway      string
	ProviderType string
}

// selectTrunks resolves which outbound trunk(s) to use for the next call
// from this tenant, respecting tenants.outbound_strategy:
//
//   - failover    → return all active trunks, lowest-priority first;
//                   caller pipe-joins them in &bridge(...) and FS rolls
//                   over according to continue_on_fail.
//   - manual      → return only the lowest-priority active trunk. Operator
//                   uses PATCH /v1/numbers/{id} to flip `active` and
//                   thereby switch primary.
//   - round_robin → return one trunk; cycle via in-memory counter so
//                   successive calls land on successive trunks.
//   - load_based  → reserved; returns 501.
//   - empty list  → fine — caller falls back to the dev loopback path.
func (s *Server) selectTrunks(ctx context.Context, t *Tenant) ([]trunk, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT n.e164, tk.id, tk.provider_type
		FROM numbers n
		JOIN trunks tk ON tk.id = n.trunk_id
		WHERE n.tenant_id = $1
		  AND n.type = 'outbound'
		  AND n.active
		  AND tk.active
		ORDER BY n.priority ASC, n.created_at ASC
	`, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list outbound trunks: %w", err)
	}
	defer rows.Close()

	var all []trunk
	for rows.Next() {
		var (
			e164     string
			trunkID  uuid.UUID
			provType string
		)
		if err := rows.Scan(&e164, &trunkID, &provType); err != nil {
			return nil, fmt.Errorf("scan trunk: %w", err)
		}
		all = append(all, trunk{
			E164:         e164,
			Gateway:      freeswitch.GatewayName(trunkID),
			ProviderType: provType,
		})
	}
	if len(all) == 0 {
		return nil, nil
	}

	strategy := t.OutboundStrategy
	if strategy == "" {
		strategy = "failover"
	}

	switch strategy {
	case "failover":
		return all, nil
	case "manual":
		// Lowest-priority active trunk only. Operator controls failover
		// by flipping `numbers.active` via PATCH /v1/numbers/{id}.
		return all[:1], nil
	case "round_robin":
		idx := s.nextRoundRobin(t.ID, len(all))
		return []trunk{all[idx]}, nil
	case "load_based":
		// Reserved for Phase 3 — needs per-trunk success-rate tracking.
		// The schema accepts the value so adding the impl later is purely
		// additive.
		return nil, errors.New("load_based strategy is reserved and not yet implemented")
	default:
		return nil, fmt.Errorf("unknown outbound_strategy %q", strategy)
	}
}

// nextRoundRobin returns the next index in [0, n) for round_robin tenants.
// In-memory counter; resets on process restart, which is acceptable for
// load-spreading purposes (a few extra calls land on one trunk after a
// restart; the cycle re-establishes immediately).
func (s *Server) nextRoundRobin(tenantID uuid.UUID, n int) int {
	if n <= 0 {
		return 0
	}
	s.rrMu.Lock()
	defer s.rrMu.Unlock()
	if s.rr == nil {
		s.rr = make(map[uuid.UUID]int)
	}
	idx := s.rr[tenantID] % n
	s.rr[tenantID] = (idx + 1) % n
	return idx
}

// Counters live on Server; declared as a package-level helper so the
// Server struct definition in server.go stays the single source of truth.
// The mutex+map pair is initialised lazily in nextRoundRobin.
type roundRobinState struct {
	mu sync.Mutex
	m  map[uuid.UUID]int
}
