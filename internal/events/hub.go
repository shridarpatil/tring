package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shridarpatil/tring/internal/cache"
)

const subBuffer = 16

// Subscriber is one connected SSE client's frame queue.
type Subscriber struct {
	tenant string
	ch     chan []byte
}

// Frames is the channel the SSE handler reads pre-rendered frames from. Closed
// when the hub drops or unsubscribes this subscriber.
func (s *Subscriber) Frames() <-chan []byte { return s.ch }

// recomputer rebuilds a derived frame (stats/queues) on a debounce. Defined here;
// scheduling is wired in Task 3.
type recomputer struct {
	out      string
	debounce time.Duration
	build    func(ctx context.Context, tenantID string) ([]byte, error)
}

// Hub subscribes to the Redis event bus once and fans events to connected SSE
// subscribers, grouped by tenant. A nil *Hub is a valid no-op.
type Hub struct {
	rdb *redis.Client
	log *slog.Logger

	mu      sync.Mutex
	subs    map[string]map[*Subscriber]struct{}
	forward map[string]bool
	recomp  map[string][]*recomputer
	pending map[string]bool

	// after schedules f after d. Overridable in tests (see Task 3).
	after func(d time.Duration, f func())
}

// NewHub dials Redis at addr. Empty addr → (nil, nil): live streaming disabled
// (the SSE handler still serves the connect-time snapshot).
func NewHub(addr, password string, logger *slog.Logger) (*Hub, error) {
	if addr == "" {
		return nil, nil
	}
	rdb := redis.NewClient(cache.Options(addr, password))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		rdb:     rdb,
		log:     logger.With("component", "events.hub"),
		subs:    map[string]map[*Subscriber]struct{}{},
		forward: map[string]bool{},
		recomp:  map[string][]*recomputer{},
		pending: map[string]bool{},
		after:   func(d time.Duration, f func()) { time.AfterFunc(d, f) },
	}, nil
}

// Forward marks event types that are fanned to subscribers verbatim. Call before
// Run.
func (h *Hub) Forward(types ...string) {
	if h == nil {
		return
	}
	for _, t := range types {
		h.forward[t] = true
	}
}

// Subscribe registers a client for a tenant's events. Returns nil on a nil hub.
func (h *Hub) Subscribe(tenantID string) *Subscriber {
	if h == nil {
		return nil
	}
	sub := &Subscriber{tenant: tenantID, ch: make(chan []byte, subBuffer)}
	h.mu.Lock()
	if h.subs[tenantID] == nil {
		h.subs[tenantID] = map[*Subscriber]struct{}{}
	}
	h.subs[tenantID][sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// Unsubscribe removes a client and closes its channel. Safe on nil hub/sub.
func (h *Hub) Unsubscribe(sub *Subscriber) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	if set := h.subs[sub.tenant]; set != nil {
		if _, ok := set[sub]; ok {
			delete(set, sub)
			close(sub.ch)
			if len(set) == 0 {
				delete(h.subs, sub.tenant)
			}
		}
	}
	h.mu.Unlock()
}

// dispatch sends a rendered frame to every subscriber of a tenant. A subscriber
// whose buffer is full is dropped (it reconnects and re-snapshots).
func (h *Hub) dispatch(tenant string, frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[tenant] {
		select {
		case sub.ch <- frame:
		default:
			delete(h.subs[tenant], sub)
			close(sub.ch)
			h.log.Warn("events: dropping slow subscriber", "tenant", tenant)
		}
	}
	if len(h.subs[tenant]) == 0 {
		delete(h.subs, tenant)
	}
}

// handle processes one decoded event: forward it if configured, and trigger any
// recomputers (scheduling wired in Task 3).
func (h *Hub) handle(tenant string, evt Event) {
	if h.forward[evt.Type] {
		h.dispatch(tenant, Frame(evt.Type, evt.Data))
	}
	for _, rc := range h.recomp[evt.Type] {
		h.schedule(tenant, rc)
	}
}

// Recompute registers a derived-frame builder triggered by the given event
// types. Concurrent triggers for the same (tenant, out) are coalesced into one
// build after debounce. Call before Run.
func (h *Hub) Recompute(triggers []string, out string, debounce time.Duration, build func(ctx context.Context, tenantID string) ([]byte, error)) {
	if h == nil {
		return
	}
	rc := &recomputer{out: out, debounce: debounce, build: build}
	for _, t := range triggers {
		h.recomp[t] = append(h.recomp[t], rc)
	}
}

func (h *Hub) schedule(tenant string, rc *recomputer) {
	key := tenant + "|" + rc.out
	h.mu.Lock()
	if h.pending[key] {
		h.mu.Unlock()
		return
	}
	h.pending[key] = true
	h.mu.Unlock()

	h.after(rc.debounce, func() {
		h.mu.Lock()
		delete(h.pending, key)
		h.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		data, err := rc.build(ctx, tenant)
		if err != nil {
			h.log.Warn("events: recompute failed", "out", rc.out, "tenant", tenant, "err", err)
			return
		}
		h.dispatch(tenant, Frame(rc.out, data))
	})
}

// NewTestHub builds a Redis-less Hub for tests (no Run; dispatch/handle work).
func NewTestHub() *Hub {
	return &Hub{
		log:     slog.Default(),
		subs:    map[string]map[*Subscriber]struct{}{},
		forward: map[string]bool{},
		recomp:  map[string][]*recomputer{},
		pending: map[string]bool{},
		after:   func(d time.Duration, f func()) { time.AfterFunc(d, f) },
	}
}

// DispatchForTest pushes a pre-rendered frame to a tenant's subscribers.
func (h *Hub) DispatchForTest(tenant string, frame []byte) { h.dispatch(tenant, frame) }

// Run subscribes to the bus and dispatches until ctx is done. No-op on nil hub.
func (h *Hub) Run(ctx context.Context) {
	if h == nil {
		return
	}
	pubsub := h.rdb.PSubscribe(ctx, channelPrefix+"*")
	defer func() { _ = pubsub.Close() }()
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			var evt Event
			if err := json.Unmarshal([]byte(m.Payload), &evt); err != nil {
				h.log.Warn("events: bad payload", "err", err)
				continue
			}
			tenant := evt.TenantID
			if tenant == "" {
				tenant = strings.TrimPrefix(m.Channel, channelPrefix)
			}
			h.handle(tenant, evt)
		}
	}
}

// Close releases the Redis client. Safe on nil hub.
func (h *Hub) Close() error {
	if h == nil {
		return nil
	}
	return h.rdb.Close()
}
