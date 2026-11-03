// Package cluster shares call-plane state between tring nodes through Redis:
// a registry of live nodes (refreshed by a heartbeat) and, per node, which
// agents hold a presence socket there, which of them are Away, and how many
// calls/dials each agent has on that node.
//
// Every key is a per-node contribution with a TTL, rewritten in full on each
// heartbeat. A crashed node's contribution therefore disappears on its own when
// its keys expire, and a wiped Redis is repopulated within one heartbeat —
// nothing has to be reconciled. Readers only sum keys of nodes whose registry
// hash is still alive.
//
// A nil *Cluster means single-node mode: it is always Alone and every write is a
// no-op, so callers keep using their local state.
package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
)

// Config configures a Cluster. Zero values get the documented defaults.
type Config struct {
	Addr, Password string
	ID             string // "" → <hostname>-<6 random hex>
	Mode           string // "all" | "gateway"
	Version        string
	Prefix         string        // key prefix; "" → "tring:"
	Interval       time.Duration // heartbeat; 0 → 5s
	TTL            time.Duration // every key this node writes; 0 → 15s
}

// Cluster is this node's handle on the shared registry and agent state.
type Cluster struct {
	cfg Config
	rdb *redis.Client
	log *slog.Logger

	mu     sync.Mutex
	live   []string            // live node ids from the last successful heartbeat, self first
	alone  bool                // as of the last heartbeat; true until one succeeds
	keys   map[string]struct{} // keys this node wrote, so Close can remove them
	closed bool
}

// New dials Redis and returns a Cluster. An empty Addr returns (nil, nil):
// single-node mode. An unreachable Redis returns an error.
func New(cfg Config, log *slog.Logger) (*Cluster, error) {
	if cfg.Addr == "" {
		return nil, nil
	}
	if cfg.ID == "" {
		id, err := defaultID()
		if err != nil {
			return nil, err
		}
		cfg.ID = id
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "tring:"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}

	rdb := redis.NewClient(cache.Options(cfg.Addr, cfg.Password))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return &Cluster{
		cfg:   cfg,
		rdb:   rdb,
		log:   log.With("component", "cluster", "node", cfg.ID),
		alone: true,
		keys:  map[string]struct{}{},
	}, nil
}

// defaultID is unique per process so a restarted node never inherits the
// previous run's still-unexpired keys.
func defaultID() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "node"
	}
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cluster: node id: %w", err)
	}
	return host + "-" + hex.EncodeToString(b), nil
}

// ID returns this node's id ("" on nil).
func (c *Cluster) ID() string {
	if c == nil {
		return ""
	}
	return c.cfg.ID
}

// Alone reports whether this node should behave as a single node. It does no
// I/O: it is true on nil, until a heartbeat has succeeded, after any failed
// heartbeat, and when the last heartbeat saw no other live node.
func (c *Cluster) Alone() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alone
}

// Heartbeat refreshes this node's registry entry and rewrites its whole
// contribution from s, then refreshes the cached live-node list. Tenants that
// left s are not deleted here; their keys expire after TTL.
func (c *Cluster) Heartbeat(ctx context.Context, s Snapshot) error {
	if c == nil {
		return nil
	}
	if err := c.heartbeat(ctx, s); err != nil {
		// Without a fresh view of the cluster, local state is the safe answer.
		c.mu.Lock()
		c.alone = true
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *Cluster) heartbeat(ctx context.Context, s Snapshot) error {
	id, ttl := c.cfg.ID, c.cfg.TTL
	written := map[string]struct{}{}

	// MULTI so a reader never sees a set between its DEL and SADD.
	_, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		nk := c.nodeKey(id)
		p.HSet(ctx, nk, "id", id, "mode", c.cfg.Mode, "version", c.cfg.Version,
			"legs", s.Legs, "draining", s.Draining)
		p.PExpire(ctx, nk, ttl)
		p.SAdd(ctx, c.key("nodes"), id)
		written[nk] = struct{}{}

		writeSets := func(sets map[string][]string, keyFn func(tenant, node string) string) {
			for tenant, sips := range sets {
				k := keyFn(tenant, id)
				p.Del(ctx, k)
				if len(sips) == 0 {
					continue
				}
				p.SAdd(ctx, k, toAny(sips)...)
				p.PExpire(ctx, k, ttl)
				written[k] = struct{}{}
			}
		}
		writeSets(s.Online, c.agentsKey)
		writeSets(s.Away, c.awayKey)

		for kind, m := range s.Counts {
			for ts, n := range m {
				if n <= 0 {
					continue
				}
				tenant, sip, ok := splitTenantSIP(ts)
				if !ok {
					continue
				}
				k := c.countKey(kind, tenant, sip, id)
				p.Set(ctx, k, n, ttl)
				written[k] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cluster: heartbeat write: %w", err)
	}

	members, err := c.rdb.SMembers(ctx, c.key("nodes")).Result()
	if err != nil {
		return fmt.Errorf("cluster: list nodes: %w", err)
	}
	exists := make([]*redis.IntCmd, len(members))
	if _, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, m := range members {
			exists[i] = p.Exists(ctx, c.nodeKey(m))
		}
		return nil
	}); err != nil {
		return fmt.Errorf("cluster: check nodes: %w", err)
	}

	// Self first, so reads prefer this node when an agent is online on several.
	live := []string{id}
	var dead []any
	for i, m := range members {
		switch {
		case exists[i].Val() == 0:
			dead = append(dead, m)
		case m != id:
			live = append(live, m)
		}
	}
	if len(dead) > 0 {
		// The registry set has no TTL; prune it so it doesn't grow with every
		// restarted node id.
		if err := c.rdb.SRem(ctx, c.key("nodes"), dead...).Err(); err != nil {
			c.log.Warn("cluster: prune dead nodes failed", "err", err)
		}
	}

	c.mu.Lock()
	c.live = live
	c.alone = len(live) == 1
	c.keys = written
	c.mu.Unlock()
	return nil
}

// Close removes this node's keys (best-effort) so peers stop counting it
// immediately instead of after TTL, then closes the Redis client. Calling it
// again is a no-op.
func (c *Cluster) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	keys := make([]string, 0, len(c.keys))
	for k := range c.keys {
		keys = append(keys, k)
	}
	c.alone = true
	c.live = nil
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.SRem(ctx, c.key("nodes"), c.cfg.ID)
		p.Del(ctx, c.nodeKey(c.cfg.ID))
		if len(keys) > 0 {
			p.Del(ctx, keys...)
		}
		return nil
	})
	if err != nil {
		c.log.Warn("cluster: removing node keys failed", "err", err)
	}
	return c.rdb.Close()
}

func (c *Cluster) key(s string) string { return c.cfg.Prefix + s }

func (c *Cluster) nodeKey(node string) string { return c.cfg.Prefix + "node:" + node }

func (c *Cluster) agentsKey(tenant, node string) string {
	return c.cfg.Prefix + "agents:" + tenant + ":" + node
}

func (c *Cluster) awayKey(tenant, node string) string {
	return c.cfg.Prefix + "away:" + tenant + ":" + node
}

func (c *Cluster) countKey(k Kind, tenant, sip, node string) string {
	return c.cfg.Prefix + "count:" + string(k) + ":" + tenant + ":" + sip + ":" + node
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
