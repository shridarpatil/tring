// Package presence tracks which agents are online via the media gateway.
//
// Since the ADR-001 cutover, agents connect to the gateway over a WebSocket
// instead of registering SIP with FreeSWITCH — so "is this agent online?" can
// no longer be answered by a SIP registration lookup. The gateway publishes a
// short-lived Redis key per connected agent (refreshed while the socket is up,
// gone within TTL of a disconnect/crash); the API reads it for the console's
// online badge and the orchestrator reads it to ring only connected agents on
// an IVR transfer.
//
// Backed by the generic internal/cache (Redis). A nil *Store — or one built on
// a nil cache (Redis disabled) — reports Enabled() == false so callers fall
// back to their pre-cutover behaviour instead of treating every agent as
// offline.
package presence

import (
	"context"
	"time"

	"github.com/whatomate/tring/internal/cache"
)

// TTL is how long an online marker survives without a refresh. The gateway
// refreshes well within this window; a crashed gateway's agents age out in TTL.
const TTL = 30 * time.Second

// RefreshInterval is how often the gateway should re-mark a live agent online.
const RefreshInterval = 10 * time.Second

// Store reads/writes agent presence markers in Redis.
type Store struct {
	c *cache.Cache
}

// New wraps a cache. A nil cache yields a disabled store (Enabled() == false).
func New(c *cache.Cache) *Store {
	return &Store{c: c}
}

// Enabled reports whether presence tracking is active (Redis configured).
// When false, callers must NOT treat agents as offline — fall back to the
// pre-gateway behaviour (e.g. ring all agents / show "unknown").
func (s *Store) Enabled() bool {
	return s != nil && s.c != nil
}

func key(tenant, sip string) string {
	return "presence:agent:" + tenant + ":" + sip
}

// MarkOnline (re)asserts that an agent is online, refreshing the TTL.
func (s *Store) MarkOnline(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	s.c.Set(ctx, key(tenant, sip), []byte("1"), TTL)
}

// Offline clears an agent's online marker (best-effort).
func (s *Store) Offline(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	_ = s.c.Delete(ctx, key(tenant, sip))
}

// IsOnline reports whether an agent currently has a live marker. Only
// meaningful when Enabled().
func (s *Store) IsOnline(ctx context.Context, tenant, sip string) bool {
	if !s.Enabled() {
		return false
	}
	_, ok := s.c.Get(ctx, key(tenant, sip))
	return ok
}
