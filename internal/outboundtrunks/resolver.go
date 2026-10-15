// Package outboundtrunks is the gateway's TrunkResolver (internal/media/signaling)
// implementation: it resolves a tenant's already-selected ordered outbound
// trunks for an agent-initiated web-call dial, reusing the SAME selection
// discipline (internal/trunkselect) and priority query
// (select-outbound-trunks) that internal/api's FreeSWITCH-path originate
// uses — one definition of "which trunk(s) does this tenant dial through",
// shared by both planes.
package outboundtrunks

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/trunkselect"
)

// Resolver implements signaling.TrunkResolver over the DB. Round-robin state
// is owned HERE, per-tenant, not in trunkselect (which stays a pure selection
// function) — mirrors internal/api's nextRoundRobin.
type Resolver struct {
	db *db.DB
	q  *queries.Queries

	rrMu sync.Mutex
	rr   map[uuid.UUID]int
}

// New builds a Resolver over an existing DB handle.
func New(database *db.DB) *Resolver {
	return &Resolver{db: database, q: queries.MustLoad()}
}

// OutboundTrunks returns the tenant's already-selected ordered outbound
// trunks (trunkselect.Select applied to tenants.outbound_strategy), the same
// selection the FreeSWITCH-path originate makes in internal/api. Empty result
// (nil, nil) means no active outbound trunk is configured — the caller
// (signaling.handleAgentDial) treats that as "no outbound trunk".
func (r *Resolver) OutboundTrunks(ctx context.Context, tenant uuid.UUID) ([]trunkselect.Trunk, error) {
	rows, err := r.db.Pool.Query(ctx, r.q.SelectOutboundTrunks, tenant)
	if err != nil {
		return nil, fmt.Errorf("list outbound trunks: %w", err)
	}
	defer rows.Close()

	var all []trunkselect.Trunk
	for rows.Next() {
		var (
			e164     string
			trunkID  uuid.UUID
			provType string
		)
		if err := rows.Scan(&e164, &trunkID, &provType); err != nil {
			return nil, fmt.Errorf("scan trunk: %w", err)
		}
		all = append(all, trunkselect.Trunk{
			E164: e164,
			Name: freeswitch.GatewayName(trunkID),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list outbound trunks: %w", err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	// get-tenant returns id, name, sip_domain, outbound_strategy,
	// failover_causes — only outbound_strategy matters here, but pgx needs a
	// destination for every column.
	var (
		id              uuid.UUID
		name, sipDomain string
		strategy        string
		failoverCauses  []string
	)
	if err := r.db.Pool.QueryRow(ctx, r.q.GetTenant, tenant).Scan(
		&id, &name, &sipDomain, &strategy, &failoverCauses,
	); err != nil {
		return nil, fmt.Errorf("load tenant outbound_strategy: %w", err)
	}
	if strategy == "" {
		strategy = "failover"
	}

	rrIndex := 0
	if strategy == "round_robin" {
		rrIndex = r.nextRR(tenant, len(all))
	}
	return trunkselect.Select(all, trunkselect.Strategy(strategy), rrIndex)
}

// nextRR returns the next index in [0, n) for round_robin tenants. In-memory,
// per-tenant; resets on process restart — acceptable for load-spreading, same
// precedent as internal/api's nextRoundRobin.
func (r *Resolver) nextRR(tenant uuid.UUID, n int) int {
	if n <= 0 {
		return 0
	}
	r.rrMu.Lock()
	defer r.rrMu.Unlock()
	if r.rr == nil {
		r.rr = make(map[uuid.UUID]int)
	}
	idx := r.rr[tenant] % n
	r.rr[tenant] = (idx + 1) % n
	return idx
}

// OwnsNumber reports whether e164 is one of tenant's active numbers (any type).
// Gates an explicit caller-ID override so a tenant only presents numbers it holds.
func (r *Resolver) OwnsNumber(ctx context.Context, tenant uuid.UUID, e164 string) (bool, error) {
	var ok bool
	if err := r.db.Pool.QueryRow(ctx, r.q.TenantOwnsNumber, tenant, e164).Scan(&ok); err != nil {
		return false, fmt.Errorf("check number ownership: %w", err)
	}
	return ok, nil
}
