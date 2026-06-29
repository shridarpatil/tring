package routing

import (
	"context"
	"log/slog"
	"time"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/events"
	"github.com/shridarpatil/tring/internal/presence"
)

// Router owns the transport-agnostic queue/ring discipline both planes share:
// enqueue → (caller-alive) → filter → order → reserve → ring → hold/rejoin →
// deadline, publishing depth as it goes. The plane supplies the transport
// Effects (how to ring/hold/detect-answer); everything else is one implementation
// so the FreeSWITCH and pion paths can't drift.
type Router struct {
	queue   Queue
	reserve Reservations
	rr      RRCounter
	pres    *presence.Store
	cache   *cache.Cache
	events  events.Sink
	log     *slog.Logger
}

// NewRouter builds a Router from the shared backends (Redis or in-memory).
func NewRouter(q Queue, res Reservations, rr RRCounter, pres *presence.Store, c *cache.Cache, pub events.Sink, log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{queue: q, reserve: res, rr: rr, pres: pres, cache: c, events: pub, log: log.With("component", "routing")}
}

// RouteConfig is the per-call routing parameters (from the team config + node).
type RouteConfig struct {
	TenantID    string
	Team        string        // depth key + logging; "" → non-team (no depth published)
	Source      string        // queuekeys source (SourcePhone / SourceWeb); "" → no depth
	Strategy    string        // ring_all | round_robin | sequential | longest_wait
	RingSeconds time.Duration // per ring attempt (0 → default)
	MaxWait     time.Duration // total queue wait (0 → default)
	Priority    bool
}

const (
	defaultMaxWait     = 2 * time.Minute
	defaultRingAttempt = 30 * time.Second
	reserveBuffer      = 5 * time.Second
)

// Outcome values returned by Run.
const (
	OutcomeCompleted    = "completed"     // an agent answered
	OutcomeQueueTimeout = "queue_timeout" // max_wait elapsed
	OutcomeAbandoned    = ""              // caller left while queued (terminal)
)

// Run drives one caller through the team queue: it joins the line, holds while
// waiting, and rings the available agents when it reaches the head, retrying
// until someone answers or max_wait elapses. `candidates` returns the current
// candidate SIPs (team members, or all online agents for ring-all) — re-read each
// iteration so newly-online agents are seen. Returns the outcome, whether the
// caller was transferred away, and an attribution label.
func (r *Router) Run(ctx context.Context, cfg RouteConfig, candidates func(context.Context) []string, eff Effects) (string, bool, string) {
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = defaultMaxWait
	}
	if cfg.RingSeconds <= 0 {
		cfg.RingSeconds = defaultRingAttempt
	}

	qkey := QueueKey(cfg.TenantID, cfg.Team)
	waiter := r.queue.Enqueue(ctx, qkey, cfg.Priority)
	defer func() {
		bg := context.Background()
		r.queue.Remove(bg, qkey, waiter)
		r.publishDepth(bg, cfg)
	}()

	deadline := time.Now().Add(cfg.MaxWait)
	// holdReady breaks the hold early: an available candidate is head-of-line, or
	// the wait is up. The Hold effect polls this each tick (FS watcher / pion
	// ticker), so it's also where we refresh the published depth's TTL — a single
	// gapless FS hold can outlast the depth key otherwise, blanking the dashboard.
	holdReady := func() bool {
		r.publishDepth(ctx, cfg)
		if time.Now().After(deadline) || ctx.Err() != nil {
			return true
		}
		return len(r.available(ctx, cfg, candidates)) > 0 && r.queue.IsHead(ctx, qkey, waiter)
	}

	for {
		// Async-answer backstop: a pion agent may have joined mid-hold.
		if ans, label := eff.Answered(); ans {
			return OutcomeCompleted, false, label
		}
		if !eff.CallerAlive(ctx) {
			return OutcomeAbandoned, false, "abandoned in queue"
		}

		avail := r.available(ctx, cfg, candidates)
		if len(avail) > 0 && r.queue.IsHead(ctx, qkey, waiter) {
			r.queue.Remove(ctx, qkey, waiter) // free our spot so the next waiter can claim too
			r.publishDepth(ctx, cfg)

			ordered := OrderByStrategy(ctx, r.pres, r.rr, cfg.TenantID, cfg.Team, cfg.Strategy, avail)
			release := r.reserveAgents(ctx, cfg.TenantID, ordered, cfg.RingSeconds+reserveBuffer)
			res, err := eff.Ring(ctx, ordered, cfg.RingSeconds)
			release()
			if err != nil {
				r.log.Warn("ring attempt failed", "team", cfg.Team, "err", err)
				return OutcomeAbandoned, false, ""
			}
			if res.Transferred {
				return OutcomeAbandoned, true, res.Label
			}
			if res.Answered {
				return OutcomeCompleted, false, res.Label
			}
			// Nobody answered (raced busy / declined) — rejoin the line and fall
			// through to the deadline check + hold so a never-answering-but-available
			// agent can't spin the loop.
			waiter = r.queue.Enqueue(ctx, qkey, cfg.Priority)
		}

		if time.Now().After(deadline) {
			return OutcomeQueueTimeout, false, ""
		}

		r.publishDepth(ctx, cfg)
		// Hold up to the remaining wait, returning early when holdReady() trips.
		// Passing the full remaining window (not a fixed poll) lets the FreeSWITCH
		// plane play ONE gapless hold-music stream broken by its watcher, while the
		// pion plane just polls holdReady on its own ticker.
		eff.Hold(ctx, time.Until(deadline), holdReady)
		if ctx.Err() != nil {
			return OutcomeAbandoned, false, ""
		}
	}
}

// available filters the current candidates by presence + reservation.
func (r *Router) available(ctx context.Context, cfg RouteConfig, candidates func(context.Context) []string) []string {
	return FilterAvailable(ctx, r.pres, r.reserve, cfg.TenantID, candidates(ctx))
}

// reserveAgents marks the agents being rung so a concurrent dequeue queues
// instead of double-picking them; returns a release run on a fresh context (it
// fires after a Sync ring when the call ctx may be done; the TTL is the backstop).
func (r *Router) reserveAgents(ctx context.Context, tenantID string, sips []string, ttl time.Duration) func() {
	for _, sip := range sips {
		r.reserve.Reserve(ctx, ResKey(tenantID, sip), ttl)
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for _, sip := range sips {
			r.reserve.Release(rctx, ResKey(tenantID, sip))
		}
	}
}

func (r *Router) publishDepth(ctx context.Context, cfg RouteConfig) {
	if cfg.Team == "" || cfg.Source == "" {
		return
	}
	depth := r.queue.Depth(ctx, QueueKey(cfg.TenantID, cfg.Team))
	PublishDepth(ctx, r.cache, cfg.TenantID, cfg.Team, cfg.Source, depth)
	if r.events != nil {
		r.events.Publish(ctx, cfg.TenantID, events.TypeQueueDepth, map[string]any{
			"team":   cfg.Team,
			"source": cfg.Source,
			"depth":  depth,
		})
	}
}
