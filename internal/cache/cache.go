// Package cache is a thin, generic Redis-backed key/value cache.
//
// It deals only in keys, byte values, and TTLs — it knows nothing about
// flows, tenants, or any domain type. Callers own their key namespaces and
// (de)serialization; this package just stores bytes with an expiry. That
// keeps it reusable for whatever else needs caching later (resolved tenant
// config, ICE servers, rate-limit counters, …).
//
// Redis is OPTIONAL. A nil *Cache is valid and behaves as a no-op (caching
// disabled), so self-hosters who don't run Redis still get correct — just
// uncached — behaviour. Reads and writes are best-effort: a Redis error is
// treated as a miss / dropped write so a transient cache problem never turns
// into a request failure. Deletes (invalidation) return their error so the
// caller can decide whether stale data matters.
package cache

import (
	"context"
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

// Delete removes a key (cache invalidation). Returns any Redis error so the
// caller can log it — a failed delete means readers see stale data until the
// TTL expires. A nil cache is a no-op.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c == nil {
		return nil
	}
	return c.rdb.Del(ctx, key).Err()
}

// Close releases the Redis connection pool. Safe on a nil cache.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.rdb.Close()
}
