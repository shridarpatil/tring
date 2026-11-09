package brain

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/routing"
)

// pollEffects answers any ring, and holds by polling ready like the pion plane.
type pollEffects struct{}

func (pollEffects) Ring(context.Context, []string, time.Duration) (routing.RingOutcome, error) {
	return routing.RingOutcome{Answered: true}, nil
}
func (pollEffects) Hold(ctx context.Context, max time.Duration, ready func() bool) {
	end := time.Now().Add(max)
	for !ready() && ctx.Err() == nil && time.Now().Before(end) {
		time.Sleep(5 * time.Millisecond)
	}
}
func (pollEffects) Answered() (bool, string)         { return false, "" }
func (pollEffects) CallerAlive(context.Context) bool { return true }

// busyTransport offers every requested agent except the busy ones.
type busyTransport struct{ busy map[string]bool }

func (b busyTransport) Prepare(_ context.Context, _ string, _ ivrdrive.Target, _ bool, members func(context.Context) []string, _, _ string) (func(context.Context) []string, routing.Effects, func()) {
	return func(ctx context.Context) []string {
		var out []string
		for _, s := range members(ctx) {
			if !b.busy[s] {
				out = append(out, s)
			}
		}
		return out
	}, pollEffects{}, func() {}
}

// A transfer waiting for a busy agent must not hold up a transfer to a free
// one. Direct-agent transfers have no team, and all queued on one tenant-wide
// line — prod 2026-11-04: a transfer to a free agent waited ~2 min behind one to
// a busy agent.
func TestDirectTransferNotBlockedByAnotherAgentsQueue(t *testing.T) {
	r := newRouter(busyTransport{busy: map[string]bool{"bob": true}})
	tenant := uuid.New().String()

	waitBob, stopBob := context.WithCancel(context.Background())
	defer stopBob()
	go r.RingNoHandoff(waitBob, tenant+"/call-1", ivrdrive.Target{AgentSIP: "bob"})
	time.Sleep(50 * time.Millisecond) // bob's caller is queued first

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, _ := r.RingNoHandoff(ctx, tenant+"/call-2", ivrdrive.Target{AgentSIP: "alice"}); out != "completed" {
		t.Fatalf("transfer to free alice = %q, want completed (blocked behind bob's queue?)", out)
	}
}

// liveTeams is a Teams whose membership can change mid-call.
type liveTeams struct {
	mu    sync.Mutex
	sips  []string
	reads int
}

func (l *liveTeams) set(sips ...string) { l.mu.Lock(); l.sips = sips; l.mu.Unlock() }
func (l *liveTeams) TeamSIPs(context.Context, uuid.UUID, string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads++
	return append([]string(nil), l.sips...), nil
}
func (l *liveTeams) TeamConfig(context.Context, uuid.UUID, string) (routing.TeamConfig, bool, error) {
	return routing.TeamConfig{Strategy: "ring_all", RingSeconds: 5, MaxWait: 30}, true, nil
}

// A caller queued for a team with no members must be rung as soon as an agent
// is added to the team — membership is re-read during the wait, not snapshotted
// when the caller entered the queue.
func TestQueuedCallerRingsAgentAddedToTeam(t *testing.T) {
	teams := &liveTeams{}
	ar := NewAgentRouter(teams,
		routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil, nil),
		busyTransport{}, nil, nil)
	tenant := uuid.New().String()

	go func() {
		time.Sleep(50 * time.Millisecond)
		teams.set("alice")
		ar.MembersChanged(tenant)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out, _ := ar.RingNoHandoff(ctx, tenant+"/call", ivrdrive.Target{Team: "sales"}); out != "completed" {
		t.Fatalf("outcome = %q, want completed (agent added to team was never rung)", out)
	}
}

// While membership doesn't change, a queued caller reads its team once — the
// hold loop's frequent candidate checks must not each hit the DB.
func TestQueuedCallerReadsTeamOnceUntilChanged(t *testing.T) {
	teams := &liveTeams{}
	ar := NewAgentRouter(teams,
		routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil, nil),
		busyTransport{}, nil, nil)
	tenant := uuid.New().String()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ar.RingNoHandoff(ctx, tenant+"/call", ivrdrive.Target{Team: "sales"}) // waits out ctx: team stays empty
	if teams.reads != 1 {
		t.Fatalf("team read %d times while unchanged, want 1", teams.reads)
	}

	ar.MembersChanged(uuid.New().String()) // another tenant's change is not ours
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	teams.reads = 0
	ar.RingNoHandoff(ctx2, tenant+"/call-2", ivrdrive.Target{Team: "sales"})
	if teams.reads != 1 {
		t.Fatalf("team read %d times after another tenant's change, want 1", teams.reads)
	}
}

// ringRecorder never answers and records who each ring attempt offered.
type ringRecorder struct {
	mu   sync.Mutex
	rung [][]string
}

func (r *ringRecorder) Ring(_ context.Context, sips []string, _ time.Duration) (routing.RingOutcome, error) {
	r.mu.Lock()
	r.rung = append(r.rung, append([]string(nil), sips...))
	r.mu.Unlock()
	return routing.RingOutcome{}, nil
}
func (r *ringRecorder) Hold(ctx context.Context, max time.Duration, ready func() bool) {
	pollEffects{}.Hold(ctx, max, ready)
}
func (*ringRecorder) Answered() (bool, string)         { return false, "" }
func (*ringRecorder) CallerAlive(context.Context) bool { return true }

type recTransport struct{ eff *ringRecorder }

func (t recTransport) Prepare(_ context.Context, _ string, _ ivrdrive.Target, _ bool, members func(context.Context) []string, _, _ string) (func(context.Context) []string, routing.Effects, func()) {
	return members, t.eff, func() {}
}

// An agent removed from a team while a caller waits is not rung again.
func TestQueuedCallerSkipsAgentRemovedFromTeam(t *testing.T) {
	teams := &liveTeams{}
	teams.set("alice", "bob")
	eff := &ringRecorder{}
	ar := NewAgentRouter(teams,
		routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil, nil),
		recTransport{eff: eff}, nil, nil)
	tenant := uuid.New().String()

	go func() {
		time.Sleep(30 * time.Millisecond)
		teams.set("alice")
		ar.MembersChanged(tenant)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	ar.RingNoHandoff(ctx, tenant+"/call", ivrdrive.Target{Team: "sales"})

	eff.mu.Lock()
	defer eff.mu.Unlock()
	if len(eff.rung) < 2 {
		t.Fatalf("ring attempts = %d, want several", len(eff.rung))
	}
	last := eff.rung[len(eff.rung)-1]
	if len(last) != 1 || last[0] != "alice" {
		t.Fatalf("last ring offered %v, want [alice] (bob was removed)", last)
	}
}
