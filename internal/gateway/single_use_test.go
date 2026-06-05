package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeJTIStore is an in-memory jtiStore: SetNX returns (true, nil) the first
// time it sees a key, (false, nil) thereafter — mirroring Redis SETNX without a
// Redis dependency. A non-nil err makes every call return it, to model an
// unreachable store.
type fakeJTIStore struct {
	seen map[string]bool
	err  error
}

func (f *fakeJTIStore) SetNX(_ context.Context, key string, _ []byte, _ time.Duration) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.seen[key] {
		return false, nil
	}
	f.seen[key] = true
	return true, nil
}

// TestConsumeTokenSingleUse: the gateway consumes a gateway-call-token's jti on
// first use and rejects any replay within its TTL.
func TestConsumeTokenSingleUse(t *testing.T) {
	g := &Gateway{tokens: &fakeJTIStore{seen: map[string]bool{}}}
	ctx := context.Background()
	exp := time.Now().Add(2 * time.Minute)

	if err := g.consumeToken(ctx, "jti-1", exp); err != nil {
		t.Fatalf("first use of jti-1 must be allowed: %v", err)
	}
	if err := g.consumeToken(ctx, "jti-1", exp); !errors.Is(err, errTokenReused) {
		t.Fatalf("replay of jti-1 must be rejected as reused, got %v", err)
	}
	if err := g.consumeToken(ctx, "jti-2", exp); err != nil {
		t.Fatalf("a different jti must be allowed: %v", err)
	}
}

// TestConsumeTokenFailsClosed: single-use is a hard gate. A token with no jti,
// an unreachable store, or no store at all (Redis off) must all be REJECTED, so
// a captured token can never be replayed during a cache outage. This is the
// fail-CLOSED contract — Redis is a hard dependency.
func TestConsumeTokenFailsClosed(t *testing.T) {
	ctx := context.Background()
	future := time.Now().Add(2 * time.Minute)

	withStore := &Gateway{tokens: &fakeJTIStore{seen: map[string]bool{}}}
	if err := withStore.consumeToken(ctx, "", future); err == nil {
		t.Error("a token with no jti must be rejected")
	}
	if err := withStore.consumeToken(ctx, "jti-expired", time.Now().Add(-time.Minute)); err == nil {
		t.Error("a token with no remaining TTL must be rejected")
	}

	down := &Gateway{tokens: &fakeJTIStore{err: errors.New("redis down")}}
	if err := down.consumeToken(ctx, "jti-x", future); err == nil {
		t.Error("an unreachable store must fail closed (reject)")
	}
	if errors.Is(down.consumeToken(ctx, "jti-x", future), errTokenReused) {
		t.Error("a store error must NOT be reported as a reuse (it's a 503, not a 401)")
	}

	noStore := &Gateway{} // tokens == nil (Redis disabled)
	if err := noStore.consumeToken(ctx, "jti-x", future); err == nil {
		t.Error("a nil store must fail closed (reject), not allow the call")
	}
}
