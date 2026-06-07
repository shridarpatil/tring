// Package cache is a thin, generic Redis-backed key/value cache.
//
// It deals only in keys, byte values, and TTLs — it knows nothing about
// flows, tenants, or any domain type. Callers own their key namespaces and
// (de)serialization; this package just stores bytes with an expiry. That
// keeps it reusable for whatever else needs caching later (resolved tenant
// config, ICE servers, rate-limit counters, …).
//
// Redis is OPTIONAL for the best-effort surface. A nil *Cache is valid and
// behaves as a no-op (caching disabled), so self-hosters who don't run Redis
// still get correct — just uncached — behaviour. Reads and writes are
// best-effort: a Redis error is treated as a miss / dropped write so a
// transient cache problem never turns into a request failure. Two methods opt
// OUT of best-effort and return their error so the caller decides: Delete
// (invalidation — stale data may matter) and SetNX (single-use markers — a
// caller cannot treat an unreachable Redis as "first use" without opening a
// replay window). For those, Redis is a hard dependency.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache wraps a Redis client. Construct with New; a nil *Cache is a valid
// no-op cache (every read misses, every write/delete is a no-op).
type Cache struct {
	rdb *redis.Client
}

// New dials Redis at addr (host:port) and verifies the connection with a
// short ping. An empty addr returns (nil, nil) — caching disabled. A
// non-empty addr that can't be reached returns an error so the caller can
// decide whether to fail hard or log-and-disable.
func New(addr string) (*Cache, error) {
	if addr == "" {
		return nil, nil
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return &Cache{rdb: rdb}, nil
}

// Get returns the bytes stored at key. A nil cache, a cache miss, or any
// Redis error all return ok=false, so the caller falls back to its source of
// truth — the cache never turns a transient Redis problem into a failure.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	b, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false // redis.Nil (miss) or transport error
	}
	return b, true
}

// Set stores val at key with the given TTL. Best-effort: a nil cache or a
// Redis error is ignored, since the caller already holds the value and a
// failed write just means the next read misses again. A ttl <= 0 stores the
// key without expiry — pass a positive TTL unless you mean that.
func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if c == nil {
		return
	}
	_ = c.rdb.Set(ctx, key, val, ttl).Err()
}

// SetNX atomically sets key→val with the given TTL only if the key is absent.
// Used for single-use markers — e.g. consuming a one-time token's jti. Returns
// (true, nil) when it set the key (the FIRST writer), (false, nil) when the key
// already existed (a replay), and (false, err) when the operation could not be
// performed at all — a nil cache (Redis not configured) or a Redis transport
// error.
//
// Unlike the best-effort reads/writes above, SetNX does NOT swallow the error:
// a caller enforcing single-use cannot treat "couldn't reach Redis" as "first
// use" without opening a replay window, so it MUST surface the error and let
// the caller decide. The gateway fails CLOSED on it (see gateway.consumeToken);
// Redis is therefore a hard dependency for single-use enforcement.
func (c *Cache) SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error) {
	if c == nil {
		return false, errors.New("cache: redis not configured")
	}
	return c.rdb.SetNX(ctx, key, val, ttl).Result()
}

// Delete removes a key (cache invalidation). Returns any Redis error so the
// caller can log it — a failed delete means readers see stale data until the
// TTL expires. A nil cache is a no-op.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c == nil {
		return nil
	}
	return c.rdb.Del(ctx, key).Err()
}

// ─── List ops (FIFO queues) ─────────────────────────────────────────
//
// These back the orchestrator's cross-process call queue. Still generic Redis
// primitives (no domain types). Best-effort like the K/V reads/writes: a nil
// cache or a Redis error degrades to "empty list" rather than failing — the
// caller (a queued caller's poll loop) then just keeps waiting.

// RPush appends val to the list at key (FIFO tail) and refreshes the list's TTL
// so a fully-abandoned queue self-expires. Best-effort.
func (c *Cache) RPush(ctx context.Context, key, val string, ttl time.Duration) {
	if c == nil {
		return
	}
	pipe := c.rdb.TxPipeline()
	pipe.RPush(ctx, key, val)
	pipe.Expire(ctx, key, ttl)
	_, _ = pipe.Exec(ctx)
}

// LHead returns the first (oldest) element of the list at key — the head of
// line. ok=false on a nil cache, empty list, or Redis error.
func (c *Cache) LHead(ctx context.Context, key string) (string, bool) {
	if c == nil {
		return "", false
	}
	s, err := c.rdb.LIndex(ctx, key, 0).Result()
	if err != nil {
		return "", false
	}
	return s, true
}

// LRem removes all occurrences of val from the list at key (tickets are unique,
// so this drops exactly the one waiter). Best-effort.
func (c *Cache) LRem(ctx context.Context, key, val string) {
	if c == nil {
		return
	}
	_ = c.rdb.LRem(ctx, key, 0, val).Err()
}

// LLen returns the list length (0 on a nil cache, miss, or error).
func (c *Cache) LLen(ctx context.Context, key string) int {
	if c == nil {
		return 0
	}
	n, err := c.rdb.LLen(ctx, key).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

// Close releases the Redis connection pool. Safe on a nil cache.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.rdb.Close()
}
