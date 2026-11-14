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

// reservationSpy records, per Ring, which agents the router had reserved while
// that ring was live; it never answers and cancels the route after n rings.
type reservationSpy struct {
	res      Reservations
	tenant   string
	all      []string
	n        int
	cancel   context.CancelFunc
	rings    [][]string
	reserved [][]string
}

func (*reservationSpy) Answered() (bool, string)                         { return false, "" }
func (*reservationSpy) CallerAlive(ctx context.Context) bool             { return ctx.Err() == nil }
func (*reservationSpy) Hold(context.Context, time.Duration, func() bool) {}
func (s *reservationSpy) Ring(ctx context.Context, ordered []string, _ time.Duration) (RingOutcome, error) {
	var held []string
	for _, sip := range s.all {
		if s.res.Reserved(ctx, ResKey(s.tenant, sip)) {
			held = append(held, sip)
		}
	}
	s.rings = append(s.rings, append([]string{}, ordered...))
	s.reserved = append(s.reserved, held)
	if len(s.rings) >= s.n {
		s.cancel()
	}
	return RingOutcome{}, nil
}

// A one-at-a-time strategy rings one agent per attempt, so it must reserve only
// that agent: reserving the whole team hid the other idle agents from every
// other queued caller for the full ring window. Successive attempts still walk
// the team, then start a new round.
func TestRouterOneAtATimeReservesOnlyTheRungAgent(t *testing.T) {
	for _, strategy := range []string{"sequential", "round_robin", "longest_wait"} {
		t.Run(strategy, func(t *testing.T) {
			res := NewMemReservations()
			r := NewRouter(NewMemQueue(), res, NewMemRR(), nil, nil, nil, nil)
			team := []string{"a", "b", "c"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Four attempts: a full round plus the wrap into the next.
			spy := &reservationSpy{res: res, tenant: "t1", all: team, n: 4, cancel: cancel}
			cfg := RouteConfig{TenantID: "t1", Team: "sales", Strategy: strategy, RingSeconds: time.Millisecond, MaxWait: time.Minute}
			r.Run(ctx, cfg, func(context.Context) []string { return team }, spy)

			if len(spy.rings) != 4 {
				t.Fatalf("ring attempts = %d, want 4", len(spy.rings))
			}
			seen := map[string]bool{}
			for i, rung := range spy.rings {
				if len(rung) != 1 {
					t.Fatalf("attempt %d rang %v, want exactly one agent", i+1, rung)
				}
				if len(spy.reserved[i]) != 1 || spy.reserved[i][0] != rung[0] {
					t.Fatalf("attempt %d rang %v but reserved %v, want only the rung agent", i+1, rung, spy.reserved[i])
				}
				if i < 3 {
					seen[rung[0]] = true
				}
			}
			if len(seen) != 3 {
				t.Fatalf("first round rang %v, want every agent once", spy.rings[:3])
			}
		})
	}
}

// ring_all still reserves every agent it rings.
func TestRouterRingAllReservesEveryRungAgent(t *testing.T) {
	res := NewMemReservations()
	r := NewRouter(NewMemQueue(), res, NewMemRR(), nil, nil, nil, nil)
	team := []string{"a", "b", "c"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &reservationSpy{res: res, tenant: "t1", all: team, n: 1, cancel: cancel}
	cfg := RouteConfig{TenantID: "t1", Team: "sales", Strategy: "ring_all", RingSeconds: time.Millisecond, MaxWait: time.Minute}
	r.Run(ctx, cfg, func(context.Context) []string { return team }, spy)
	if len(spy.rings) != 1 || len(spy.rings[0]) != 3 || len(spy.reserved[0]) != 3 {
		t.Fatalf("rings = %v reserved = %v, want all three rung and reserved", spy.rings, spy.reserved)
	}
}
