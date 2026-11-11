package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shridarpatil/tring/internal/queries"
)

// Store persists campaigns and atomically assigns each dial to one gateway.
type Store struct {
	Limits Limits
	Pool   *pgxpool.Pool
	Q      *queries.Queries
}

// New constructs a store over the existing application database.
func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, Q: queries.MustLoad(), Limits: DefaultLimits()}
}

type scanner interface{ Scan(...any) error }

func scanJSON[T any](row scanner) (T, error) {
	var v T
	var b []byte
	if err := row.Scan(&b); err != nil {
		return v, err
	}
	err := json.Unmarshal(b, &v)
	return v, err
}

// Get returns a tenant's campaign and recipient counts.
func (s *Store) Get(ctx context.Context, tenant, id uuid.UUID) (Campaign, error) {
	c, err := scanJSON[Campaign](s.Pool.QueryRow(ctx, s.Q.CampaignGet, tenant, id))
	if err != nil {
		return c, err
	}
	var metrics []byte
	if err = s.Pool.QueryRow(ctx, s.Q.CampaignMetrics, tenant, id).Scan(&metrics); err != nil {
		return c, err
	}
	if err = json.Unmarshal(metrics, &c.Metrics); err != nil {
		return c, err
	}
	c.Counts = map[string]int{}
	rows, err := s.Pool.Query(ctx, s.Q.CampaignCounts, tenant, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err = rows.Scan(&status, &n); err != nil {
			return c, err
		}
		c.Counts[status] = n
	}
	return c, rows.Err()
}

// Create saves a validated draft.
func (s *Store) Create(ctx context.Context, tenant uuid.UUID, name string, c Config) (uuid.UUID, error) {
	var id uuid.UUID
	if err := validateName(name); err != nil {
		return id, err
	}
	if err := c.Validate(); err != nil {
		return id, err
	}
	err := s.Pool.QueryRow(ctx, s.Q.CampaignCreate, tenant, strings.TrimSpace(name), JSON(c)).Scan(&id)
	return id, err
}
func validateName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return errors.New("name must be 1–200 bytes")
	}
	return nil
}

// Update replaces a draft and invalidates its prepared content.
func (s *Store) Update(ctx context.Context, tenant, id uuid.UUID, name string, c Config, revision int) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, s.Q.CampaignUpdate, tenant, id, strings.TrimSpace(name), JSON(c), revision)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// Prepare stores verified content only if the draft hasn't changed during rendering.
func (s *Store) Prepare(ctx context.Context, tenant, id uuid.UUID, p Prepared, revision int) error {
	tag, err := s.Pool.Exec(ctx, s.Q.CampaignPrepare, tenant, id, JSON(p), revision)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// Transition applies lifecycle controls under the same row lock as dispatch.
func (s *Store) Transition(ctx context.Context, tenant, id uuid.UUID, action string, at *time.Time, revision int) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := scanJSON[Campaign](tx.QueryRow(ctx, s.Q.CampaignLock, tenant, id))
	if err != nil {
		return err
	}
	if revision != c.Revision {
		return ErrConflict
	}
	status := ""
	switch action {
	case "start", "schedule":
		if c.Status != "draft" || c.Prepared == nil {
			return ErrConflict
		}
		var count int
		if err = tx.QueryRow(ctx, s.Q.CampaignRecipientCount, tenant, id, "").Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return errors.New("import at least one recipient")
		}
		status = "running"
		if action == "schedule" {
			if at == nil || !at.After(time.Now()) {
				return errors.New("scheduled_at must be in the future")
			}
			status = "scheduled"
		} else {
			at = nil
		}
	case "pause":
		if c.Status != "running" && c.Status != "scheduled" {
			return ErrConflict
		}
		status = "paused"
		at = c.ScheduledAt
	case "resume":
		if c.Status != "paused" {
			return ErrConflict
		}
		status = "running"
		at = c.ScheduledAt
		if at != nil && at.After(time.Now()) {
			status = "scheduled"
		}
	case "cancel":
		if c.Status == "completed" || c.Status == "cancelled" || c.Status == "failed" {
			return ErrConflict
		}
		status = "cancelled"
		at = c.ScheduledAt
	default:
		return errors.New("unknown campaign action")
	}
	if _, err = tx.Exec(ctx, s.Q.CampaignState, tenant, id, status, at); err != nil {
		return err
	}
	if status == "cancelled" {
		if _, err = tx.Exec(ctx, s.Q.CampaignCancelQueued, tenant, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, s.Q.CampaignCancelRecipients, tenant, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Import adds validated, normalized recipients in one transaction; duplicate phones are skipped.
func (s *Store) Import(ctx context.Context, tenant, id uuid.UUID, recipients []Recipient) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	c, err := scanJSON[Campaign](tx.QueryRow(ctx, s.Q.CampaignLock, tenant, id))
	if err != nil {
		return 0, err
	}
	if c.Status != "draft" {
		return 0, ErrConflict
	}
	added := 0
	for _, r := range recipients {
		tag, err := tx.Exec(ctx, s.Q.CampaignImport, tenant, id, r.Phone, r.ExternalRef)
		if err != nil {
			return 0, err
		}
		added += int(tag.RowsAffected())
	}
	// Imports participate in optimistic launch checks as well as content edits.
	if _, err = tx.Exec(ctx, s.Q.CampaignState, tenant, id, c.Status, c.ScheduledAt); err != nil {
		return 0, err
	}
	return added, tx.Commit(ctx)
}

// List returns paginated campaigns.
func (s *Store) List(ctx context.Context, tenant uuid.UUID, limit, offset int) ([]Campaign, int, error) {
	var count int
	if err := s.Pool.QueryRow(ctx, s.Q.CampaignCount, tenant).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, s.Q.CampaignList, tenant, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, e := scanJSON[Campaign](rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, c)
	}
	return out, count, rows.Err()
}

// Recipients returns a filtered recipient page.
func (s *Store) Recipients(ctx context.Context, tenant, id uuid.UUID, limit, offset int, status string) ([]Recipient, int, error) {
	var count int
	if err := s.Pool.QueryRow(ctx, s.Q.CampaignRecipientCount, tenant, id, status).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, s.Q.CampaignRecipients, tenant, id, limit, offset, status)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Recipient{}
	for rows.Next() {
		r, e := scanJSON[Recipient](rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, r)
	}
	return out, count, rows.Err()
}

// Attempts returns paginated attempt history, including draft test calls.
func (s *Store) Attempts(ctx context.Context, tenant, id uuid.UUID, limit, offset int) ([]Attempt, int, error) {
	var count int
	if err := s.Pool.QueryRow(ctx, s.Q.CampaignAttemptCount, tenant, id).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, s.Q.CampaignAttempts, tenant, id, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		a, e := scanJSON[Attempt](rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, a)
	}
	return out, count, rows.Err()
}

// Claim consumes a queued attempt exactly once, before any carrier side effect.
// An ambiguous active attempt is never re-claimed, even after a gateway restart.
func (s *Store) Claim(ctx context.Context, tenant, id uuid.UUID) (Attempt, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Attempt{}, false, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, s.Q.CampaignWorkerLock).Scan(&locked); err != nil || !locked {
		return Attempt{}, false, err
	}
	tenants, err := s.tenantIDs(ctx, tx)
	if err != nil {
		return Attempt{}, false, err
	}
	starts, live, active, tenantActive := 0, 0, 0, 0
	for _, t := range tenants {
		var n int
		if err = tx.QueryRow(ctx, s.Q.CampaignRecentStarts, t).Scan(&n); err != nil {
			return Attempt{}, false, err
		}
		starts += n
		if err = tx.QueryRow(ctx, s.Q.CampaignRunningSlots, t).Scan(&n); err != nil {
			return Attempt{}, false, err
		}
		active += n
		if t == tenant {
			tenantActive = n
		}
		if err = tx.QueryRow(ctx, s.Q.CampaignLiveCalls, t).Scan(&n); err != nil {
			return Attempt{}, false, err
		}
		live += n
	}
	if starts >= s.Limits.StartsPerSecond || active >= s.Limits.Concurrent || tenantActive >= s.Limits.PerTenant || live+active >= s.Limits.TotalCalls-s.Limits.ReservedCalls {
		return Attempt{}, false, nil
	}
	a, err := scanJSON[Attempt](tx.QueryRow(ctx, s.Q.CampaignAttemptGet, tenant, id))
	if err != nil {
		return a, false, err
	}
	c, err := scanJSON[Campaign](tx.QueryRow(ctx, s.Q.CampaignLock, tenant, a.CampaignID))
	if err != nil {
		return a, false, err
	}
	if a.Status != "queued" {
		return a, false, nil
	}
	if c.Status == "cancelled" || c.Status == "failed" || (a.RecipientID != nil && (c.Status != "running" || !c.Config.InWindow(time.Now()))) {
		return a, false, nil
	}
	if a.RecipientID != nil && c.NextStartAt.After(time.Now()) {
		return a, false, nil
	}
	var payload []byte
	if err = tx.QueryRow(ctx, s.Q.CampaignAttemptClaim, tenant, id).Scan(&payload); errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	} else if err != nil {
		return a, false, err
	}
	if err = json.Unmarshal(payload, &a.Payload); err != nil {
		return a, false, err
	}
	if a.RecipientID != nil {
		if _, err = tx.Exec(ctx, s.Q.CampaignStartAt, tenant, c.ID, 60.0/float64(c.Config.CallsPerMinute)); err != nil {
			return a, false, err
		}
	}
	a.Status = "active"
	return a, true, tx.Commit(ctx)
}

// SetCall attaches the normal call log to an attempt before dialing.
func (s *Store) SetCall(ctx context.Context, tenant, id, callID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, s.Q.CampaignAttemptCall, tenant, id, callID)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// Answered records carrier answer independently from message completion.
func (s *Store) Answered(ctx context.Context, tenant, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, s.Q.CampaignAttemptAnswered, tenant, id)
	return err
}

// Heartbeat renews liveness. Failure means the gateway must stop this attempt.
func (s *Store) Heartbeat(ctx context.Context, tenant, id uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, s.Q.CampaignHeartbeat, tenant, id)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

// Finish settles once. Only busy/no-answer/transient failures are retried; an
// unknown outcome or partially played message never automatically redials.
func (s *Store) Finish(ctx context.Context, tenant, id uuid.UUID, status, reason string) error {
	switch status {
	case "completed", "incomplete", "busy", "no_answer", "failed", "unknown", "cancelled":
	default:
		return errors.New("invalid attempt outcome")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, err := scanJSON[Attempt](tx.QueryRow(ctx, s.Q.CampaignAttemptGet, tenant, id))
	if err != nil {
		return err
	}
	c, err := scanJSON[Campaign](tx.QueryRow(ctx, s.Q.CampaignLock, tenant, a.CampaignID))
	if err != nil {
		return err
	}
	var recipient *uuid.UUID
	var payload []byte
	var attempt int
	err = tx.QueryRow(ctx, s.Q.CampaignAttemptFinish, tenant, id, status, reason).Scan(&recipient, &payload, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "unknown" && a.CallID != nil {
		if _, err = tx.Exec(ctx, s.Q.CampaignRecoverCall, tenant, *a.CallID); err != nil {
			return err
		}
	}
	var p Payload
	if err = json.Unmarshal(payload, &p); err != nil {
		return err
	}
	if recipient != nil {
		next := status
		if (status == "busy" || status == "no_answer" || (status == "failed" && reason == "temporary_failure")) && attempt < p.Config.MaxAttempts && c.Status != "cancelled" {
			next = "pending"
		}
		if _, err = tx.Exec(ctx, s.Q.CampaignRecipientFinish, tenant, *recipient, next, p.Config.RetryMinutes*60); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
