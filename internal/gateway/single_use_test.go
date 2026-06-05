package gateway

import (
	"context"
	"testing"
	"time"
)

// fakeJTIStore is an in-memory jtiStore: SetNX is true the first time it sees a
// key, false thereafter — mirroring Redis SETNX without a Redis dependency.
type fakeJTIStore struct{ seen map[string]bool }

func (f *fakeJTIStore) SetNX(_ context.Context, key string, _ []byte, _ time.Duration) bool {
	if f.seen[key] {
		return false
	}
	f.seen[key] = true
	return true
}

// TestConsumeTokenSingleUse: the gateway consumes a gateway-call-token's jti on
// first use and rejects any replay within its TTL.
func TestConsumeTokenSingleUse(t *testing.T) {
	g := &Gateway{tokens: &fakeJTIStore{seen: map[string]bool{}}}
	ctx := context.Background()
	exp := time.Now().Add(2 * time.Minute)

	if !g.consumeToken(ctx, "jti-1", exp) {
		t.Fatal("first use of jti-1 must be allowed")
	}
	if g.consumeToken(ctx, "jti-1", exp) {
		t.Fatal("replay of jti-1 must be rejected (single-use)")
	}
	if !g.consumeToken(ctx, "jti-2", exp) {
		t.Fatal("a different jti must be allowed")
	}
}

// TestConsumeTokenAllowsWhenUncheckable: an empty jti, an already-expired token,
// or no store (Redis off) all fail OPEN — single-use never blocks a call it
// can't meaningfully gate (the JWT exp check + concurrency cap cover those).
func TestConsumeTokenAllowsWhenUncheckable(t *testing.T) {
	ctx := context.Background()
	future := time.Now().Add(2 * time.Minute)

	withStore := &Gateway{tokens: &fakeJTIStore{seen: map[string]bool{}}}
	if !withStore.consumeToken(ctx, "", future) {
		t.Error("empty jti must be allowed")
	}
	if !withStore.consumeToken(ctx, "jti-expired", time.Now().Add(-time.Minute)) {
		t.Error("an already-expired token must not be blocked by single-use")
	}

	noStore := &Gateway{} // tokens == nil (Redis disabled)
	if !noStore.consumeToken(ctx, "jti-x", future) {
		t.Error("nil store must fail open (allow)")
	}
	// And a replay against the nil store is still allowed (no enforcement).
	if !noStore.consumeToken(ctx, "jti-x", future) {
		t.Error("nil store must allow even a replay (single-use disabled)")
	}
}
