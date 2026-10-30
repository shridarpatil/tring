package api

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// errInternal marks a server-side failure (DB, I/O) rather than a problem with
// the caller's input.
var errInternal = errors.New("internal error")

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
//     caller pipe-joins them in &bridge(...) and FS rolls
//     over according to continue_on_fail.
//   - manual      → return only the lowest-priority active trunk. Operator
//     uses PATCH /v1/numbers/{id} to flip `active` and
//     thereby switch primary.
//   - round_robin → return one trunk; cycle via in-memory counter so
//     successive calls land on successive trunks.
//   - load_based  → reserved; returns 501.
//   - empty list  → fine — caller falls back to the dev loopback path.
func (s *Server) selectTrunks(ctx context.Context, t *Tenant) ([]trunk, error) {
	rows, err := s.db.Pool.Query(ctx, s.q.SelectOutboundTrunks, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list outbound trunks: %w: %w", errInternal, err)
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
			return nil, fmt.Errorf("scan trunk: %w: %w", errInternal, err)
		}
		all = append(all, trunk{
			E164:         e164,
			Gateway:      freeswitch.GatewayName(trunkID),
			ProviderType: provType,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list outbound trunks: %w: %w", errInternal, err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	strategy := t.OutboundStrategy
	if strategy == "" {
		strategy = "failover"
	}

	rrIdx := 0
	if strategy == "round_robin" {
		rrIdx = s.nextRoundRobin(t.ID, len(all))
	}
	idxs, err := trunkselect.SelectIndices(len(all), trunkselect.Strategy(strategy), rrIdx)
	if err != nil {
		return nil, err
	}
	out := make([]trunk, len(idxs))
	for i, x := range idxs {
		out[i] = all[x]
	}
	return out, nil
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
