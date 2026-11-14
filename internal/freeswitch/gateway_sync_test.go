package freeswitch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRescan stands in for ESL: it fails the first `fail` calls (all of them
// when fail < 0) and records the stale list each call was given.
type fakeRescan struct {
	mu    sync.Mutex
	fail  int
	calls [][]string
	ok    chan struct{} // closed on the first success
}

func newFakeRescan(fail int) *fakeRescan { return &fakeRescan{fail: fail, ok: make(chan struct{})} }

func (f *fakeRescan) rescan(_ context.Context, stale []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slices.Clone(stale))
	if f.fail < 0 || len(f.calls) <= f.fail {
		return errors.New("esl: connection refused")
	}
	select {
	case <-f.ok:
	default:
		close(f.ok)
	}
	return nil
}

func (f *fakeRescan) snapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A rescan that fails on the request path is retried in the background with
// the same stale gateways, so a changed or deleted trunk is still unloaded.
func TestGatewaySyncRetriesFailedRescan(t *testing.T) {
	fake := newFakeRescan(2)
	g := NewGatewaySync(fake.rescan, []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	if err := g.Sync(ctx, []string{"t_a"}); err == nil {
		t.Fatal("Sync returned nil on a failed rescan; the API must still report it")
	}
	select {
	case <-fake.ok:
	case <-time.After(5 * time.Second):
		t.Fatalf("rescan never retried to success; calls = %v", fake.snapshot())
	}
	calls := fake.snapshot()
	if len(calls) != 3 {
		t.Fatalf("rescan calls = %d, want 3 (1 failed sync + 1 failed retry + 1 ok)", len(calls))
	}
	if !slices.Equal(calls[2], []string{"t_a"}) {
		t.Fatalf("retry stale = %v, want [t_a] (the changed gateway must still be killed)", calls[2])
	}
}

// After the backoff schedule is spent the retrier stops and says so loudly.
func TestGatewaySyncGivesUp(t *testing.T) {
	fake := newFakeRescan(-1)
	var logs syncBuffer
	g := NewGatewaySync(fake.rescan, []time.Duration{time.Millisecond, time.Millisecond}, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	_ = g.Sync(ctx, []string{"t_a"})
	waitFor(t, "give-up log", func() bool { return strings.Contains(logs.String(), "giving up") })
	if n := len(fake.snapshot()); n != 3 {
		t.Fatalf("rescan calls = %d, want 3 (1 sync + 2 retries)", n)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("give-up not logged at error level:\n%s", logs.String())
	}
}

// A later sync carries gateways a failed one never got to unload: their XML is
// already on disk, so WriteGateways won't report them as stale again.
func TestGatewaySyncCarriesStaleForward(t *testing.T) {
	fake := newFakeRescan(1)
	g := NewGatewaySync(fake.rescan, nil, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	_ = g.Sync(ctx, []string{"t_a"})
	if err := g.Sync(ctx, []string{"t_b"}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	calls := fake.snapshot()
	if got := calls[1]; !slices.Equal(got, []string{"t_a", "t_b"}) {
		t.Fatalf("second sync stale = %v, want [t_a t_b]", got)
	}
	// Once applied they're not killed again on the next save.
	_ = g.Sync(ctx, nil)
	if got := fake.snapshot()[2]; len(got) != 0 {
		t.Fatalf("third sync stale = %v, want none", got)
	}
}

// Shutdown stops a pending retry rather than holding the process up.
func TestGatewaySyncStopsOnShutdown(t *testing.T) {
	fake := newFakeRescan(-1)
	g := NewGatewaySync(fake.rescan, []time.Duration{time.Hour}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.Run(ctx) }()

	_ = g.Sync(ctx, []string{"t_a"})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
}
