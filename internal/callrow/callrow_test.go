package callrow_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/callrow"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/queries"
)

// TestWrapUpQueries proves the 0039 migration applies and that the wrap-up
// queries and the widened get-call/list-calls selects agree with callrow.Scan.
func TestWrapUpQueries(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	q := queries.MustLoad()
	pool := f.DB.Pool

	var callID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO calls (tenant_id, direction, status, from_number, to_number, agent_id, leg_id, source)
		VALUES ($1, 'inbound', 'answered', '+911', '+912', $2, 'leg-1', 'phone') RETURNING id`,
		f.TenantID, f.UserID).Scan(&callID); err != nil {
		t.Fatal(err)
	}

	c, err := callrow.Get(ctx, f.DB, q, f.TenantID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Disposition != nil || c.WrappedUpAt != nil {
		t.Fatalf("fresh call has wrap-up data: %+v", c)
	}

	var dispID, tagID uuid.UUID
	if err := pool.QueryRow(ctx, q.CreateDisposition, f.TenantID, "Sale").Scan(&dispID, new(string), new(*string), new(any), new([]string)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, q.CreateCallTag, f.TenantID, "vip").Scan(&tagID, new(string), new(*string), new(any), new([]string)); err != nil {
		t.Fatal(err)
	}
	// Names are unique per tenant, case-insensitively.
	if err := pool.QueryRow(ctx, q.CreateDisposition, f.TenantID, "SALE").Scan(new(uuid.UUID), new(string), new(*string), new(any), new([]string)); err == nil {
		t.Fatal("duplicate disposition name accepted")
	}

	if _, err := pool.Exec(ctx, q.BeginCallWrapUp, f.TenantID, callID, f.UserID, true, ""); err != nil {
		t.Fatal(err)
	}
	save := func(agent uuid.UUID) error {
		return pool.QueryRow(ctx, q.SaveCallWrapUp, f.TenantID, callID, dispID, "called back", agent).Scan(new(*uuid.UUID))
	}
	if err := save(uuid.New()); err != pgx.ErrNoRows {
		t.Fatalf("save by another agent: got %v, want ErrNoRows", err)
	}
	if err := save(f.UserID); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := save(f.UserID); err != pgx.ErrNoRows {
		t.Fatalf("second save: got %v, want ErrNoRows", err)
	}
	if err := pool.QueryRow(ctx, q.TimeoutCallWrapUp, f.TenantID, callID).Scan(new(*uuid.UUID)); err != pgx.ErrNoRows {
		t.Fatalf("timeout after save: got %v, want ErrNoRows", err)
	}

	if _, err := pool.Exec(ctx, q.InsertCallTagLink, f.TenantID, callID, tagID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, q.CountActiveTagsIn, f.TenantID, []uuid.UUID{tagID, uuid.New()}, "").Scan(&n); err != nil || n != 1 {
		t.Fatalf("count-active-tags-in = %d, %v; want 1", n, err)
	}

	c, err = callrow.Get(ctx, f.DB, q, f.TenantID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Disposition == nil || c.Disposition.ID != dispID || c.Disposition.Name != "Sale" {
		t.Fatalf("disposition = %+v", c.Disposition)
	}
	if c.Notes == nil || *c.Notes != "called back" || c.WrappedUpAt == nil || c.WrapUpEnd == nil || *c.WrapUpEnd != "done" {
		t.Fatalf("wrap-up fields = %v %v %v", c.Notes, c.WrappedUpAt, c.WrapUpEnd)
	}

	list := func(disp, tag string) int {
		rows, err := pool.Query(ctx, q.ListCalls, f.TenantID, 10, 0, "", "", "", disp, tag)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			if _, err := callrow.Scan(rows); err != nil {
				t.Fatal(err)
			}
			n++
		}
		return n
	}
	if got := list("", ""); got != 1 {
		t.Fatalf("unfiltered = %d", got)
	}
	if got := list(dispID.String(), tagID.String()); got != 1 {
		t.Fatalf("matching filters = %d", got)
	}
	if got := list(uuid.NewString(), ""); got != 0 {
		t.Fatalf("other disposition = %d", got)
	}
	if got := list("", uuid.NewString()); got != 0 {
		t.Fatalf("other tag = %d", got)
	}
}
