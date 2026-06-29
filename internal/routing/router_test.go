package routing

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeEffects records the ring sequence and answers on a chosen attempt.
type fakeEffects struct {
	mu          sync.Mutex
	rings       [][]string // ordered candidates per Ring call
	answerOn    int        // 1-based attempt to answer on; 0 = never
	transferOn  int        // attempt to report a blind-transfer
	alive       bool
	answeredBak bool // backstop Answered() result
}

func (f *fakeEffects) Answered() (bool, string) { return f.answeredBak, "backstop" }
func (f *fakeEffects) CallerAlive(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive
}
func (f *fakeEffects) Ring(_ context.Context, ordered []string, _ time.Duration) (RingOutcome, error) {
	f.mu.Lock()
	f.rings = append(f.rings, append([]string{}, ordered...))
	n := len(f.rings)
	f.mu.Unlock()
	if f.transferOn == n {
		return RingOutcome{Transferred: true, Label: "blind"}, nil
	}
	if f.answerOn == n {
		return RingOutcome{Answered: true, Label: ordered[0]}, nil
	}
	return RingOutcome{}, nil
}
func (f *fakeEffects) Hold(_ context.Context, _ time.Duration, _ func() bool) { /* return immediately */
}

func newRouter() *Router {
	// No presence (passthrough filter), mem queue/reservations, mem RR.
	return NewRouter(NewMemQueue(), NewMemReservations(), NewMemRR(), nil, nil, nil, nil)
}

func TestRouterCompletesWhenAgentAnswers(t *testing.T) {
	r := newRouter()
	eff := &fakeEffects{alive: true, answerOn: 2} // first ring no-answer, second answers
	cfg := RouteConfig{TenantID: "t1", Team: "sales", Strategy: "ring_all", RingSeconds: time.Millisecond, MaxWait: time.Minute}

	out, transferred, label := r.Run(context.Background(), cfg,
		func(context.Context) []string { return []string{"a", "b"} }, eff)

	if out != OutcomeCompleted || transferred {
		t.Fatalf("out=%q transferred=%v, want completed/false", out, transferred)
	}
	if label != "a" {
		t.Fatalf("label = %q, want a", label)
	}
	if len(eff.rings) != 2 {
		t.Fatalf("expected 2 ring attempts (rejoin after the first), got %d", len(eff.rings))
	}
}

func TestRouterQueueTimeout(t *testing.T) {
	r := newRouter()
	eff := &fakeEffects{alive: true, answerOn: 0} // never answers
	cfg := RouteConfig{TenantID: "t1", Team: "sales", RingSeconds: time.Millisecond, MaxWait: 40 * time.Millisecond}

	out, _, _ := r.Run(context.Background(), cfg,
		func(context.Context) []string { return []string{"a"} }, eff)
	if out != OutcomeQueueTimeout {
		t.Fatalf("out = %q, want queue_timeout", out)
	}
}

func TestRouterAbandonedWhenCallerGone(t *testing.T) {
	r := newRouter()
	eff := &fakeEffects{alive: false} // caller already gone
	cfg := RouteConfig{TenantID: "t1", Team: "sales", MaxWait: time.Minute}

	out, _, label := r.Run(context.Background(), cfg,
		func(context.Context) []string { return []string{"a"} }, eff)
	if out != OutcomeAbandoned {
		t.Fatalf("out = %q, want abandoned", out)
	}
	if label != "abandoned in queue" {
		t.Fatalf("label = %q", label)
	}
}

func TestRouterTransferredAway(t *testing.T) {
	r := newRouter()
	eff := &fakeEffects{alive: true, transferOn: 1}
	cfg := RouteConfig{TenantID: "t1", Team: "sales", RingSeconds: time.Millisecond, MaxWait: time.Minute}

	out, transferred, _ := r.Run(context.Background(), cfg,
		func(context.Context) []string { return []string{"a"} }, eff)
	if out != OutcomeAbandoned || !transferred {
		t.Fatalf("out=%q transferred=%v, want abandoned/true", out, transferred)
	}
}

func TestRouterAsyncAnswerBackstop(t *testing.T) {
	r := newRouter()
	// No candidates ever available, but the async backstop reports an answer
	// (a pion agent joined the room mid-hold).
	eff := &fakeEffects{alive: true, answeredBak: true}
	cfg := RouteConfig{TenantID: "t1", Team: "sales", MaxWait: time.Minute}

	out, _, label := r.Run(context.Background(), cfg,
		func(context.Context) []string { return nil }, eff)
	if out != OutcomeCompleted || label != "backstop" {
		t.Fatalf("out=%q label=%q, want completed/backstop", out, label)
	}
}
