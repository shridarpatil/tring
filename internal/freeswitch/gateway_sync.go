package freeswitch

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

// GatewaySync applies trunk changes to sofia (RescanExternalProfile) and, when
// FreeSWITCH can't be reached, keeps retrying in the background. Without it a
// failed rescan left changed or deleted trunks loaded in sofia until someone
// happened to save another trunk or hit POST /v1/trunks/reload.
//
// It also remembers the stale gateways (to killgw) of a rescan that failed:
// their new XML is already on disk, so WriteGateways won't report them again
// and a later rescan would otherwise leave the old settings loaded.
type GatewaySync struct {
	rescan  func(ctx context.Context, stale []string) error
	backoff []time.Duration // wait before each background retry; then give up
	log     *slog.Logger
	kick    chan struct{} // a request-path rescan failed; buffered(1)

	mu      sync.Mutex
	pending map[string]struct{} // gateways to killgw on the next rescan
	failed  bool                // the last rescan failed; a retry is wanted
}

// NewGatewaySync wraps rescan (Client.RescanExternalProfile; a fake in tests).
// A nil backoff uses the default schedule, about 8 minutes in all — long
// enough to ride out a FreeSWITCH restart.
func NewGatewaySync(rescan func(ctx context.Context, stale []string) error, backoff []time.Duration, log *slog.Logger) *GatewaySync {
	if backoff == nil {
		backoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	}
	return &GatewaySync{
		rescan:  rescan,
		backoff: backoff,
		log:     log,
		kick:    make(chan struct{}, 1),
		pending: map[string]struct{}{},
	}
}

// Sync rescans now with stale plus any gateways a failed rescan left behind.
// On failure it returns the error (the caller must still report that FreeSWITCH
// isn't in sync) and schedules a background retry for Run to carry out.
func (g *GatewaySync) Sync(ctx context.Context, stale []string) error {
	g.mu.Lock()
	for _, name := range stale {
		g.pending[name] = struct{}{}
	}
	g.mu.Unlock()
	if err := g.attempt(ctx); err != nil {
		select {
		case g.kick <- struct{}{}:
		default: // a retry is already queued
		}
		return err
	}
	return nil
}

// Run carries out retries scheduled by Sync until ctx ends (shutdown).
func (g *GatewaySync) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.kick:
			g.retry(ctx)
		}
	}
}

// retry walks the backoff schedule until a rescan succeeds — here or on the
// request path, which clears failed — or the schedule runs out.
func (g *GatewaySync) retry(ctx context.Context) {
	for i, wait := range g.backoff {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		g.mu.Lock()
		failed := g.failed
		g.mu.Unlock()
		if !failed {
			return
		}
		err := g.attempt(ctx)
		if err == nil {
			g.log.Info("sofia rescan succeeded on retry", "attempt", i+1)
			return
		}
		if ctx.Err() != nil {
			return
		}
		g.log.Warn("sofia rescan retry failed", "attempt", i+1, "of", len(g.backoff), "err", err)
	}
	g.log.Error("sofia rescan: giving up after retries; FreeSWITCH trunks are out of sync with the DB until the next trunk save or POST /v1/trunks/reload",
		"retries", len(g.backoff))
}

// attempt runs one rescan with every pending gateway, dropping the ones it
// applied on success. Names added while it runs stay pending.
func (g *GatewaySync) attempt(ctx context.Context) error {
	g.mu.Lock()
	stale := slices.Sorted(maps.Keys(g.pending))
	g.mu.Unlock()
	err := g.rescan(ctx, stale)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failed = err != nil
	if err == nil {
		for _, name := range stale {
			delete(g.pending, name)
		}
	}
	return err
}
