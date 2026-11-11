package routing

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type racingReservations struct {
	Reservations
	arrived  chan struct{}
	proceed  chan struct{}
	attempts atomic.Int32
}

func (r *racingReservations) Reserve(ctx context.Context, key, owner string, ttl time.Duration) bool {
	if r.attempts.Add(1) <= 2 {
		r.arrived <- struct{}{}
		select {
		case <-r.proceed:
		case <-ctx.Done():
			return false
		}
	}
	return r.Reservations.Reserve(ctx, key, owner, ttl)
}

type heldRing struct{ started chan struct{} }

func (*heldRing) Answered() (bool, string)             { return false, "" }
func (*heldRing) CallerAlive(ctx context.Context) bool { return ctx.Err() == nil }
func (f *heldRing) Ring(ctx context.Context, _ []string, _ time.Duration) (RingOutcome, error) {
	f.started <- struct{}{}
	<-ctx.Done()
	return RingOutcome{}, ctx.Err()
}
func (*heldRing) Hold(ctx context.Context, _ time.Duration, _ func() bool) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Millisecond):
	}
}

func testConcurrentRoutes(t *testing.T, store Reservations) {
	t.Helper()
	res := &racingReservations{Reservations: store, arrived: make(chan struct{}, 2), proceed: make(chan struct{})}
	router := NewRouter(NewMemQueue(), res, NewMemRR(), nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eff := &heldRing{started: make(chan struct{}, 2)}
	done := make(chan struct{}, 2)
	// Different teams sharing the same agent bypass FIFO serialization. Both
	// callers observe the agent free before either attempts the atomic claim.
	for _, team := range []string{"sales", "support"} {
		go func() {
			router.Run(ctx, RouteConfig{TenantID: t.Name(), Team: team, MaxWait: time.Second}, func(context.Context) []string { return []string{"alice"} }, eff)
			done <- struct{}{}
		}()
	}
	for range 2 {
		select {
		case <-res.arrived:
		case <-time.After(time.Second):
			t.Fatal("routes did not reach acquisition")
		}
	}
	close(res.proceed)
	select {
	case <-eff.started:
	case <-time.After(time.Second):
		t.Fatal("no route rang")
	}
	select {
	case <-eff.started:
		t.Error("two callers rang the same agent")
	case <-time.After(80 * time.Millisecond):
	}
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("route did not stop")
		}
	}
	if res.Reserved(context.Background(), ResKey(t.Name(), "alice")) {
		t.Fatal("cancelled ring leaked claim")
	}
}
func TestConcurrentRoutesClaimOnce(t *testing.T) { testConcurrentRoutes(t, NewMemReservations()) }
func TestConcurrentRedisRoutesClaimOnce(t *testing.T) {
	c := testRedis(t)
	defer c.Close()
	testConcurrentRoutes(t, NewRedisReservations(c))
}

func TestReservationsApplyWithoutPresence(t *testing.T) {
	store := NewMemReservations()
	store.Reserve(context.Background(), ResKey("tenant", "alice"), "first", time.Second)
	available := FilterAvailable(context.Background(), nil, store, "tenant", []string{"alice", "bob"})
	if len(available) != 1 || available[0] != "bob" {
		t.Fatalf("available = %v, want only bob", available)
	}
}
