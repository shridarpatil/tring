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
// (invalidation — stale data may matter), SetNX and GetDel (single-use markers — a
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

// Options builds the go-redis client options for addr (host:port) and an
// optional password (Redis AUTH / requirepass; empty = no auth). Every Redis
// client in the app is built from this so connection settings can't drift
// between call sites.
func Options(addr, password string) *redis.Options {
	return &redis.Options{Addr: addr, Password: password}
}

// New dials Redis at addr (host:port), authenticating with password when
// non-empty, and verifies the connection with a short ping. An empty addr
// returns (nil, nil) — caching disabled. A non-empty addr that can't be
// reached (or rejects the password) returns an error so the caller can
// decide whether to fail hard or log-and-disable.
func New(addr, password string) (*Cache, error) {
	if addr == "" {
		return nil, nil
	}
	rdb := redis.NewClient(Options(addr, password))
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

// CompareDelete releases an owned key without deleting a replacement owner's key.
func (c *Cache) CompareDelete(ctx context.Context, key, owner string) (bool, error) {
	if c == nil {
		return false, errors.New("cache: redis not configured")
	}
	n, err := c.rdb.Eval(ctx, `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) else return 0 end`, []string{key}, owner).Int()
	return n == 1, err
}

// CompareExpire renews a lease only while it still belongs to the caller.
func (c *Cache) CompareExpire(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	if c == nil {
		return false, errors.New("cache: redis not configured")
	}
	n, err := c.rdb.Eval(ctx, `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) else return 0 end`, []string{key}, owner, ttl.Milliseconds()).Int()
	return n == 1, err
}

// GetDel atomically reads and deletes key, for single-use values such as SSO
// state and one-time codes. Returns (val, true, nil) for the one consumer,
// (nil, false, nil) on a miss (never set, expired or already consumed), and
// (nil, false, err) when Redis is not configured or unreachable.
//
// Like SetNX it does NOT swallow errors: treating "couldn't reach Redis" as a
// plain miss is harmless, but the caller must be able to tell it apart to
// answer "unavailable" rather than "invalid", and must never fall back to a
// non-consuming read that would allow replay.
func (c *Cache) GetDel(ctx context.Context, key string) ([]byte, bool, error) {
	if c == nil {
		return nil, false, errors.New("cache: redis not configured")
	}
	b, err := c.rdb.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
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

// LHeadLive returns the head of the list at key, first popping every leading
// element whose liveness key (leasePrefix+element) no longer exists. Atomic
// (one Lua script), so a concurrent RPush or LRem can't interleave with the
// sweep. ok=false on a nil cache, an empty list, or a Redis error. The lease
// keys are computed in the script, so this assumes a single Redis node, not
// Redis Cluster.
func (c *Cache) LHeadLive(ctx context.Context, key, leasePrefix string) (string, bool) {
	if c == nil {
		return "", false
	}
	s, err := c.rdb.Eval(ctx, `
while true do
  local h = redis.call('LINDEX', KEYS[1], 0)
  if not h then return false end
  if redis.call('EXISTS', ARGV[1] .. h) == 1 then return h end
  redis.call('LPOP', KEYS[1])
end`, []string{key}, leasePrefix).Text()
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
