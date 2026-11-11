package routing

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testExclusiveReservations(t *testing.T, store Reservations) {
	t.Helper()
	ctx := context.Background()
	key := "test:" + uuid.NewString()
	start := make(chan struct{})
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if store.Reserve(ctx, key, string(rune('a'+i)), time.Second) {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("successful concurrent owners = %d, want 1", wins.Load())
	}
	// Wrong-owner mutations must not free or extend the current claim.
	store.Release(ctx, key, "stale")
	if !store.Reserved(ctx, key) || store.Renew(ctx, key, "stale", time.Second) {
		t.Fatal("stale owner changed the current claim")
	}
}

func testExpiredOwner(t *testing.T, store Reservations) {
	t.Helper()
	ctx := context.Background()
	key := "test:" + uuid.NewString()
	if !store.Reserve(ctx, key, "old", 40*time.Millisecond) {
		t.Fatal("initial claim failed")
	}
	time.Sleep(80 * time.Millisecond)
	if !store.Reserve(ctx, key, "new", time.Second) {
		t.Fatal("could not reclaim expired slot")
	}
	defer store.Release(ctx, key, "new")
	store.Release(ctx, key, "old")
	if store.Renew(ctx, key, "old", time.Second) || !store.Reserved(ctx, key) {
		t.Fatal("old owner modified replacement claim")
	}
	if !store.Renew(ctx, key, "new", time.Second) {
		t.Fatal("current owner could not renew")
	}
}

func TestAtomicReservations(t *testing.T) {
	testExclusiveReservations(t, NewMemReservations())
	testExpiredOwner(t, NewMemReservations())
}
func TestAtomicRedisReservations(t *testing.T) {
	c := testRedis(t)
	defer c.Close()
	testExclusiveReservations(t, NewRedisReservations(c))
	testExpiredOwner(t, NewRedisReservations(c))
}

func TestClaimRetainedThroughAnswerAndRenewed(t *testing.T) {
	store := NewMemReservations()
	claim := acquire(context.Background(), store, "agent", 300*time.Millisecond)
	if claim == nil {
		t.Fatal("claim failed")
	}
	defer claim.Release()
	active := claim.Retain()
	if active == nil {
		t.Fatal("answer failed to retain")
	}
	defer active.Release()
	claim.Release()                    // ring ended; participant now owns the lease
	claim.Release()                    // repeat cleanup is harmless
	time.Sleep(750 * time.Millisecond) // beyond the original expiry
	if !store.Reserved(context.Background(), "agent") {
		t.Fatal("active call's claim expired")
	}
	active.Release()
	if store.Reserved(context.Background(), "agent") {
		t.Fatal("ended call leaked its claim")
	}
	if active.Retain() != nil {
		t.Fatal("released claim can be resurrected")
	}
}

type failedRenewal struct{ Reservations }

func (failedRenewal) Renew(context.Context, string, string, time.Duration) bool { return false }
func TestClaimSignalsRenewalLoss(t *testing.T) {
	store := failedRenewal{NewMemReservations()}
	claim := acquire(context.Background(), store, "agent", 300*time.Millisecond)
	if claim == nil {
		t.Fatal("claim failed")
	}
	defer claim.Release()
	select {
	case <-claim.Lost():
	case <-time.After(time.Second):
		t.Fatal("claim loss not reported")
	}
	if claim.Retain() != nil {
		t.Fatal("lost claim allowed an answer")
	}
}

func TestRedisReservationFailsClosed(t *testing.T) {
	if Acquire(context.Background(), NewRedisReservations(nil), "t", "a") != nil {
		t.Fatal("unavailable Redis granted a claim")
	}
}
