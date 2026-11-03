package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// setTimeout bounds each immediate write: they sit on the call path, and a slow
// Redis must not stall it (the next heartbeat rewrites everything anyway).
const setTimeout = time.Second

// Kind names a per-agent counter.
type Kind string

// Counter kinds.
const (
	OnCall Kind = "oncall"
	Dials  Kind = "dials"
)

// Snapshot is this node's full contribution, rewritten on every heartbeat.
type Snapshot struct {
	Legs     int
	Draining bool
	Online   map[string][]string     // tenant → sips with a presence socket on this node
	Away     map[string][]string     // tenant → online sips marked Away
	Counts   map[Kind]map[string]int // kind → "tenant|sip" → n (n > 0 only)
}

// AgentState is an agent's cluster-wide state.
type AgentState struct {
	Node   string // node holding the agent's presence socket
	Away   bool
	OnCall int // summed over live nodes
	Dials  int // summed over live nodes
}

// SetOnline adds or removes sip from this node's online set right away, so
// peers see it before the next heartbeat.
func (c *Cluster) SetOnline(ctx context.Context, tenant, sip string, online bool) {
	if c == nil {
		return
	}
	c.setMember(ctx, c.agentsKey(tenant, c.cfg.ID), sip, online)
}

// SetAway adds or removes sip from this node's away set right away.
func (c *Cluster) SetAway(ctx context.Context, tenant, sip string, away bool) {
	if c == nil {
		return
	}
	c.setMember(ctx, c.awayKey(tenant, c.cfg.ID), sip, away)
}

// SetCount writes this node's counter for the agent right away; n <= 0 deletes it.
func (c *Cluster) SetCount(ctx context.Context, k Kind, tenant, sip string, n int) {
	if c == nil {
		return
	}
	key := c.countKey(k, tenant, sip, c.cfg.ID)
	ctx, cancel := context.WithTimeout(ctx, setTimeout)
	defer cancel()
	var err error
	if n <= 0 {
		err = c.rdb.Del(ctx, key).Err()
	} else {
		err = c.rdb.Set(ctx, key, n, c.cfg.TTL).Err()
		c.remember(key)
	}
	if err != nil {
		c.log.Warn("cluster: set count failed", "kind", k, "tenant", tenant, "sip", sip, "err", err)
	}
}

func (c *Cluster) setMember(ctx context.Context, key, sip string, add bool) {
	ctx, cancel := context.WithTimeout(ctx, setTimeout)
	defer cancel()
	var err error
	if add {
		_, err = c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.SAdd(ctx, key, sip)
			p.PExpire(ctx, key, c.cfg.TTL)
			return nil
		})
		c.remember(key)
	} else {
		err = c.rdb.SRem(ctx, key, sip).Err()
	}
	if err != nil {
		c.log.Warn("cluster: set member failed", "key", key, "sip", sip, "add", add, "err", err)
	}
}

// remember records a key for Close; the next heartbeat resets the record.
func (c *Cluster) remember(key string) {
	c.mu.Lock()
	c.keys[key] = struct{}{}
	c.mu.Unlock()
}

// liveNodes returns the live list from the last heartbeat (self first).
func (c *Cluster) liveNodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

// Agents returns every agent of tenant that is online on some live node. When
// an agent is online on several nodes, Node is the first in the live list
// (this node when it is one of them).
func (c *Cluster) Agents(ctx context.Context, tenant string) (map[string]AgentState, error) {
	if c == nil {
		return nil, nil
	}
	live := c.liveNodes()
	out := map[string]AgentState{}
	if len(live) == 0 {
		return out, nil
	}

	online := make([]*redis.StringSliceCmd, len(live))
	away := make([]*redis.StringSliceCmd, len(live))
	if _, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, n := range live {
			online[i] = p.SMembers(ctx, c.agentsKey(tenant, n))
			away[i] = p.SMembers(ctx, c.awayKey(tenant, n))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("cluster: read agents: %w", err)
	}
	for i, n := range live {
		for _, sip := range online[i].Val() {
			if _, ok := out[sip]; !ok {
				out[sip] = AgentState{Node: n}
			}
		}
	}
	awaySet := map[string]bool{}
	for i := range live {
		for _, sip := range away[i].Val() {
			awaySet[sip] = true
		}
	}
	if len(out) == 0 {
		return out, nil
	}

	type countRef struct {
		sip  string
		kind Kind
		cmd  *redis.StringCmd
	}
	var refs []countRef
	if _, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for sip := range out {
			for _, n := range live {
				for _, k := range []Kind{OnCall, Dials} {
					refs = append(refs, countRef{sip, k, p.Get(ctx, c.countKey(k, tenant, sip, n))})
				}
			}
		}
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("cluster: read counts: %w", err)
	}
	for _, r := range refs {
		v, err := r.cmd.Int()
		if err != nil {
			continue // missing (redis.Nil) or not a number: contributes nothing
		}
		st := out[r.sip]
		if r.kind == OnCall {
			st.OnCall += v
		} else {
			st.Dials += v
		}
		out[r.sip] = st
	}
	for sip, st := range out {
		if awaySet[sip] {
			st.Away = true
			out[sip] = st
		}
	}
	return out, nil
}

// Count returns the agent's counter summed over live nodes.
func (c *Cluster) Count(ctx context.Context, k Kind, tenant, sip string) (int, error) {
	if c == nil {
		return 0, nil
	}
	live := c.liveNodes()
	cmds := make([]*redis.StringCmd, len(live))
	if _, err := c.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, n := range live {
			cmds[i] = p.Get(ctx, c.countKey(k, tenant, sip, n))
		}
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("cluster: read count: %w", err)
	}
	total := 0
	for _, cmd := range cmds {
		if v, err := cmd.Int(); err == nil {
			total += v
		}
	}
	return total, nil
}

// splitTenantSIP splits a Snapshot.Counts key "tenant|sip".
func splitTenantSIP(s string) (tenant, sip string, ok bool) {
	tenant, sip, ok = strings.Cut(s, "|")
	return tenant, sip, ok && tenant != "" && sip != ""
}
