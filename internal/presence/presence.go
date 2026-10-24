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
	"strconv"
	"time"

	"github.com/shridarpatil/tring/internal/cache"
)

// TTL is how long an online marker survives without a refresh. The gateway
// refreshes well within this window; a crashed gateway's agents age out in TTL.
const TTL = 30 * time.Second

// FreedTTL bounds how long an agent's last-freed timestamp is retained. Past it
// (a long idle stretch with no calls) the agent simply reads as "idle longest"
// — exactly what the longest_wait ring strategy wants — so the value is
// generous but not load-bearing.
const FreedTTL = time.Hour

// AvailabilityTTL bounds a cached available_for_calls. The API deletes the key
// on every update, so this only caps drift from a missed invalidation.
const AvailabilityTTL = 10 * time.Minute

// RefreshInterval is how often the gateway should re-mark a live agent online.
const RefreshInterval = 10 * time.Second

// Status values published per agent (the Redis key's value). Absent key = offline.
const (
	StatusOffline = "offline"
	StatusOnline  = "online"  // connected and available to ring
	StatusOnCall  = "on_call" // connected but busy on a call — don't ring
)

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

func freedKey(tenant, sip string) string {
	return "presence:freed:" + tenant + ":" + sip
}

func availabilityKey(tenant, sip string) string {
	return "presence:available:" + tenant + ":" + sip
}

// CachedAvailability returns the agent's cached available_for_calls (the
// backend Away/Break flag). ok=false on a miss — read the DB and CacheAvailability.
func (s *Store) CachedAvailability(ctx context.Context, tenant, sip string) (available, ok bool) {
	if !s.Enabled() {
		return false, false
	}
	v, ok := s.c.Get(ctx, availabilityKey(tenant, sip))
	if !ok {
		return false, false
	}
	return string(v) == "1", true
}

// CacheAvailability stores the agent's available_for_calls read from the DB.
func (s *Store) CacheAvailability(ctx context.Context, tenant, sip string, available bool) {
	if !s.Enabled() {
		return
	}
	v := "0"
	if available {
		v = "1"
	}
	s.c.Set(ctx, availabilityKey(tenant, sip), []byte(v), AvailabilityTTL)
}

// InvalidateAvailability drops the cached flag after the API updates it, so the
// next read goes to the DB.
func (s *Store) InvalidateAvailability(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	_ = s.c.Delete(ctx, availabilityKey(tenant, sip))
}

// MarkFreed records that an agent just finished a call (became free) at now.
// The longest_wait ring strategy orders candidates by this; an absent value
// sorts as "idle longest" (a freshly-connected or long-idle agent), which is
// the desired behaviour, so this never has to be perfectly durable.
func (s *Store) MarkFreed(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	ts := strconv.FormatInt(time.Now().UnixNano(), 10)
	s.c.Set(ctx, freedKey(tenant, sip), []byte(ts), FreedTTL)
}

// FreedAt returns when an agent last became free. ok=false when unknown (never
// recorded or expired) — callers treat that as "idle longest". Only meaningful
// when Enabled().
func (s *Store) FreedAt(ctx context.Context, tenant, sip string) (time.Time, bool) {
	if !s.Enabled() {
		return time.Time{}, false
	}
	v, ok := s.c.Get(ctx, freedKey(tenant, sip))
	if !ok {
		return time.Time{}, false
	}
	ns, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

// MarkOnline (re)asserts that an agent is online and AVAILABLE, refreshing TTL.
func (s *Store) MarkOnline(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	s.c.Set(ctx, key(tenant, sip), []byte(StatusOnline), TTL)
}

// MarkOnCall (re)asserts that an agent is online but BUSY on a call, refreshing
// TTL. Available() is false for such agents, so the orchestrator's ring-all
// skips them — no second call is sent to an agent already on one.
func (s *Store) MarkOnCall(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	s.c.Set(ctx, key(tenant, sip), []byte(StatusOnCall), TTL)
}

// Offline clears an agent's marker (best-effort).
func (s *Store) Offline(ctx context.Context, tenant, sip string) {
	if !s.Enabled() {
		return
	}
	_ = s.c.Delete(ctx, key(tenant, sip))
}

// Status returns "online", "on_call", or "offline". Only meaningful when Enabled().
func (s *Store) Status(ctx context.Context, tenant, sip string) string {
	if !s.Enabled() {
		return StatusOffline
	}
	v, ok := s.c.Get(ctx, key(tenant, sip))
	if !ok {
		return StatusOffline
	}
	if string(v) == StatusOnCall {
		return StatusOnCall
	}
	return StatusOnline
}

// Available reports whether an agent is a valid ring target: connected and not
// on a call. Only meaningful when Enabled().
func (s *Store) Available(ctx context.Context, tenant, sip string) bool {
	if !s.Enabled() {
		return false
	}
	v, ok := s.c.Get(ctx, key(tenant, sip))
	return ok && string(v) != StatusOnCall
}

// IsOnline reports whether an agent currently has a live marker (online or on a
// call). Only meaningful when Enabled().
func (s *Store) IsOnline(ctx context.Context, tenant, sip string) bool {
	if !s.Enabled() {
		return false
	}
	_, ok := s.c.Get(ctx, key(tenant, sip))
	return ok
}
