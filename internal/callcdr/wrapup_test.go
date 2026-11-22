package callcdr_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/callcdr"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/webhook"
)

// endedCall starts and ends a call, returning its id. Wrap-up opens on a call
// that has already ended.
func endedCall(t *testing.T, fx *dbtest.Fixture, l *callcdr.Log, name string) uuid.UUID {
	t.Helper()
	roomID := fx.TenantID.String() + "/" + name
	l.Started(context.Background(), fx.TenantID, roomID, "+919800000001", "+919800000002", "phone", "", "caller-pid")
	id := l.CallID(roomID)
	l.Ended(roomID, "caller_hangup")
	return id
}

func wrapUpEnd(t *testing.T, fx *dbtest.Fixture, callID uuid.UUID) *string {
	t.Helper()
	var end *string
	if err := fx.DB.Pool.QueryRow(context.Background(),
		`SELECT wrap_up_end FROM calls WHERE id = $1`, callID).Scan(&end); err != nil {
		t.Fatal(err)
	}
	return end
}

func TestWrapUpSettingsTeamThenTenant(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	if _, err := fx.DB.Pool.Exec(ctx,
		`INSERT INTO teams (tenant_id, name, wrap_up_seconds, require_disposition) VALUES ($1, 'sales', 45, true)`, fx.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.DB.Pool.Exec(ctx,
		`UPDATE tenants SET wrap_up_seconds = 20, require_disposition = false WHERE id = $1`, fx.TenantID); err != nil {
		t.Fatal(err)
	}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil)
	for team, want := range map[string]struct {
		secs int
		req  bool
	}{"sales": {45, true}, "": {20, false}, "deleted-team": {20, false}} {
		secs, req, err := l.WrapUpSettings(ctx, fx.TenantID, team)
		if err != nil || secs != want.secs || req != want.req {
			t.Fatalf("WrapUpSettings(%q) = %d, %v, %v; want %d, %v", team, secs, req, err, want.secs, want.req)
		}
	}
}

func TestTimeoutWrapUpOnce(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	seedAgent(t, fx, "alice")
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil)
	id := endedCall(t, fx, l, "once")
	if err := l.BeginWrapUp(ctx, fx.TenantID, id, "alice", true); err != nil {
		t.Fatal(err)
	}
	if won, err := l.TimeoutWrapUp(ctx, fx.TenantID, id); err != nil || !won {
		t.Fatalf("first timeout = %v, %v; want won", won, err)
	}
	if end := wrapUpEnd(t, fx, id); end == nil || *end != "timeout" {
		t.Fatalf("wrap_up_end = %v, want timeout", end)
	}
	if won, err := l.TimeoutWrapUp(ctx, fx.TenantID, id); err != nil || won {
		t.Fatalf("second timeout = %v, %v; want not won", won, err)
	}
}

func TestWrapUpTimeoutLosesToSave(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	agentID := seedAgent(t, fx, "alice")
	spy := &hookSpy{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(spy)
	id := endedCall(t, fx, l, "loses")
	if err := l.BeginWrapUp(ctx, fx.TenantID, id, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.DB.Pool.Exec(ctx, `UPDATE calls SET wrap_up_webhook_id = $2 WHERE id = $1`, id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := fx.DB.Pool.QueryRow(ctx, queries.MustLoad().SaveCallWrapUp, fx.TenantID, id, nil, "n", agentID).Scan(new(*uuid.UUID)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if won, err := l.TimeoutWrapUp(ctx, fx.TenantID, id); err != nil || won {
		t.Fatalf("timeout after save = %v, %v; want not won", won, err)
	}
	if end := wrapUpEnd(t, fx, id); end == nil || *end != "done" {
		t.Fatalf("wrap_up_end = %v, want done", end)
	}
	for _, e := range spy.got {
		if e == webhook.EventCallWrappedUp {
			t.Fatalf("call.wrapped_up sent after a lost timeout: %v", spy.got)
		}
	}
}

func TestTimeoutWrapUpSendsWebhook(t *testing.T) {
	fx := dbtest.Setup(t)
	ctx := context.Background()
	seedAgent(t, fx, "alice")
	spy := &hookSpy{}
	l := callcdr.New(fx.DB, nil, t.TempDir(), nil, nil).WithWebhooks(spy)
	hookID := uuid.New()
	roomID := fx.TenantID.String() + "/hook"
	l.Started(ctx, fx.TenantID, roomID, "+919800000001", "+919800000002", "phone", "", "caller-pid")
	id := l.CallID(roomID)
	l.WrapUpWebhook(roomID, hookID) // live call: stored on the row
	l.Ended(roomID, "caller_hangup")
	spy.got, spy.ids, spy.call = nil, nil, nil // drop any call.ended
	if err := l.BeginWrapUp(ctx, fx.TenantID, id, "alice", false); err != nil {
		t.Fatal(err)
	}
	if won, err := l.TimeoutWrapUp(ctx, fx.TenantID, id); err != nil || !won {
		t.Fatalf("timeout = %v, %v", won, err)
	}
	if len(spy.got) != 1 || spy.got[0] != webhook.EventCallWrappedUp || spy.ids[0] != hookID {
		t.Fatalf("sent %v to %v, want one call.wrapped_up to %s", spy.got, spy.ids, hookID)
	}
	c := spy.call[0]
	if c.WrapUpEnd == nil || *c.WrapUpEnd != "timeout" || c.ID != id {
		t.Fatalf("payload call = %+v, want wrap_up_end timeout", c)
	}
}
