package campaign

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Limits reserve gateway capacity for ordinary calls. The tenant cap also
// bounds each tenant-owned trunk, even when several campaigns share it.
type Limits struct{ Concurrent, PerTenant, StartsPerSecond, TotalCalls, ReservedCalls int }

// DefaultLimits are deliberately conservative until operators measure capacity.
func DefaultLimits() Limits { return Limits{50, 10, 5, 100, 20} }

// Worker schedules durable attempts and forwards queued IDs to the gateway.
// Multiple API replicas share a short database allocation lock; no lock is held
// while performing network I/O. Gateway ownership is a separate durable CAS.
type Worker struct {
	Store    *Store
	Limits   Limits
	Dispatch func(context.Context, uuid.UUID, uuid.UUID) error
	Log      *slog.Logger
}

func (s *Store) tenants(ctx context.Context) ([]uuid.UUID, error) {
	return s.tenantIDs(ctx, s.Pool)
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// Reuse the transaction connection: taking a second pool connection while
// holding locks can starve a small pool whose other callers await those locks.
func (s *Store) tenantIDs(ctx context.Context, db queryer) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, s.Q.CampaignTenants)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type capacity struct{ active, recent, live int }

func (s *Store) capacity(ctx context.Context, tx pgx.Tx, tenants []uuid.UUID) (map[uuid.UUID]capacity, capacity, error) {
	out := map[uuid.UUID]capacity{}
	var total capacity
	for _, id := range tenants {
		var c capacity
		if err := tx.QueryRow(ctx, s.Q.CampaignSlots, id).Scan(&c.active, &c.recent); err != nil {
			return nil, total, err
		}
		if err := tx.QueryRow(ctx, s.Q.CampaignLiveCalls, id).Scan(&c.live); err != nil {
			return nil, total, err
		}
		out[id] = c
		total.active += c.active
		total.recent += c.recent
		total.live += c.live
	}
	return out, total, nil
}
func (l Limits) room(total, tenant capacity) bool {
	return total.active < l.Concurrent && tenant.active < l.PerTenant && total.recent < l.StartsPerSecond && total.live+total.active < l.TotalCalls-l.ReservedCalls
}

// Reserve makes at most one new attempt per due campaign in a fair, rate-limited pass.
func (s *Store) Reserve(ctx context.Context, limits Limits) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, s.Q.CampaignWorkerLock).Scan(&locked); err != nil || !locked {
		return err
	}
	tenants, err := s.tenantIDs(ctx, tx)
	if err != nil {
		return err
	}
	caps, total, err := s.capacity(ctx, tx, tenants)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if _, err = tx.Exec(ctx, s.Q.CampaignComplete, tenant); err != nil {
			return err
		}
		if !limits.room(total, caps[tenant]) {
			continue
		}
		rows, err := tx.Query(ctx, s.Q.CampaignDue, tenant)
		if err != nil {
			return err
		}
		var due []Campaign
		for rows.Next() {
			c, e := scanJSON[Campaign](rows)
			if e != nil {
				rows.Close()
				return e
			}
			due = append(due, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range due {
			if !limits.room(total, caps[tenant]) {
				break
			}
			if c.Prepared == nil || !c.Config.InWindow(time.Now()) {
				continue
			}
			var active int
			if err = tx.QueryRow(ctx, s.Q.CampaignOwnSlots, tenant, c.ID).Scan(&active); err != nil {
				return err
			}
			if active >= c.Config.Concurrency {
				continue
			}
			r, err := scanJSON[Recipient](tx.QueryRow(ctx, s.Q.CampaignNextRecipient, tenant, c.ID))
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			var id uuid.UUID
			if err = tx.QueryRow(ctx, s.Q.CampaignAttemptCreate, tenant, c.ID, r.ID, r.Phone, r.ExternalRef, r.Attempts+1, JSON(Payload{c.Config, *c.Prepared})).Scan(&id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, s.Q.CampaignReserveRecipient, tenant, r.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, s.Q.CampaignDispatchAt, tenant, c.ID, 60.0/float64(c.Config.CallsPerMinute)); err != nil {
				return err
			}
			cap := caps[tenant]
			cap.active++
			cap.recent++
			caps[tenant] = cap
			total.active++
			total.recent++
		}
	}
	return tx.Commit(ctx)
}

// Test queues a real test call with the same capacity and idempotency rules.
// Draft tests deliberately bypass delivery hours; the operator requests an immediate call.
func (s *Store) Test(ctx context.Context, tenant, id uuid.UUID, phone string, revision int, limits Limits) (uuid.UUID, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, s.Q.CampaignWorkerLock).Scan(&locked); err != nil {
		return uuid.Nil, err
	}
	if !locked {
		return uuid.Nil, errors.New("scheduler busy; retry shortly")
	}
	c, err := scanJSON[Campaign](tx.QueryRow(ctx, s.Q.CampaignLock, tenant, id))
	if err != nil {
		return uuid.Nil, err
	}
	if c.Status != "draft" || c.Prepared == nil || revision != c.Revision {
		return uuid.Nil, ErrConflict
	}
	tenants, err := s.tenantIDs(ctx, tx)
	if err != nil {
		return uuid.Nil, err
	}
	caps, total, err := s.capacity(ctx, tx, tenants)
	if err != nil {
		return uuid.Nil, err
	}
	if !limits.room(total, caps[tenant]) {
		return uuid.Nil, errors.New("campaign calling capacity is full")
	}
	var attempt uuid.UUID
	err = tx.QueryRow(ctx, s.Q.CampaignAttemptCreate, tenant, id, nil, phone, "Test call", 1, JSON(Payload{c.Config, *c.Prepared})).Scan(&attempt)
	if err != nil {
		return uuid.Nil, err
	}
	// A repeated test request with the same revision is rejected.
	if _, err = tx.Exec(ctx, s.Q.CampaignState, tenant, id, c.Status, c.ScheduledAt); err != nil {
		return uuid.Nil, err
	}
	return attempt, tx.Commit(ctx)
}

// Run stops dispatching when ctx is cancelled and waits for in-flight requests.
func (w *Worker) Run(ctx context.Context) {
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, 8)
	inFlight := sync.Map{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := w.Store.Reserve(tickCtx, w.Limits)
		if err != nil {
			log.Warn("campaign scheduler failed", "err", err)
			cancel()
			continue
		}
		tenants, err := w.Store.tenants(tickCtx)
		if err != nil {
			cancel()
			continue
		}
		for _, tenant := range tenants {
			ids, err := w.Store.attemptIDs(tickCtx, w.Store.Q.CampaignStale, tenant)
			if err == nil {
				for _, id := range ids {
					if err = w.Store.Finish(tickCtx, tenant, id, "unknown", "gateway heartbeat lost; automatic retry suppressed"); err != nil {
						log.Warn("campaign recovery failed", "attempt", id, "err", err)
					}
				}
			}
			ids, err = w.Store.attemptIDs(tickCtx, w.Store.Q.CampaignQueued, tenant)
			if err != nil {
				continue
			}
			for _, id := range ids {
				if _, loaded := inFlight.LoadOrStore(id, true); loaded {
					continue
				}
				select {
				case slots <- struct{}{}:
				default:
					inFlight.Delete(id)
					continue
				}
				wg.Add(1)
				go func(tenant, id uuid.UUID) {
					defer wg.Done()
					defer func() { <-slots; inFlight.Delete(id) }()
					cctx, stop := context.WithTimeout(ctx, 3*time.Second)
					defer stop()
					if err := w.Dispatch(cctx, tenant, id); err != nil && ctx.Err() == nil {
						log.Debug("campaign dispatch deferred", "attempt", id, "err", err)
					}
				}(tenant, id)
			}
		}
		cancel()
	}
}
func (s *Store) attemptIDs(ctx context.Context, query string, tenant uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, query, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
