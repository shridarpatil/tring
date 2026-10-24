package brain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/routing"
)

type fakeTeams struct{ sips []string }

func (f fakeTeams) TeamSIPs(context.Context, uuid.UUID, string) ([]string, error) { return f.sips, nil }
func (f fakeTeams) TeamConfig(context.Context, uuid.UUID, string) (routing.TeamConfig, bool, error) {
	return routing.TeamConfig{Strategy: "ring_all", RingSeconds: 5, MaxWait: 30}, true, nil
}

// answerEffects answers on the first Ring.
type answerEffects struct{ got []string }

func (e *answerEffects) Ring(_ context.Context, ordered []string, _ time.Duration) (routing.RingOutcome, error) {
	e.got = ordered
	return routing.RingOutcome{Answered: true}, nil
}
func (e *answerEffects) Hold(context.Context, time.Duration, func() bool) {}
func (e *answerEffects) Answered() (bool, string)                          { return false, "" }
func (e *answerEffects) CallerAlive(context.Context) bool                  { return true }

type fakeTransport struct{ eff *answerEffects }

func (f *fakeTransport) Prepare(_ context.Context, _ string, _ ivrdrive.Target, _ bool, sips []string, _, _ string) (func(context.Context) []string, routing.Effects, func()) {
	return func(context.Context) []string { return sips }, f.eff, func() {}
}

// handoffTransport is a fakeTransport that also implements brain.HandoffWaiter,
// recording whether the wait was invoked and forcing its result.
type handoffTransport struct {
	fakeTransport
	resumeToCaller bool
	waited         bool
}

func (h *handoffTransport) AwaitHandoffEnd(context.Context, string) bool {
	h.waited = true
	return h.resumeToCaller
}

func newRouter(tport RingTransport) *AgentRouter {
	return NewAgentRouter(
		fakeTeams{sips: []string{"alice", "bob"}},
		routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil, nil),
		tport, nil,
	)
}

// When the transport waits for the handoff and the caller is still present after
// the agent leaves, the transfer reports "completed" so the flow continues.
func TestAgentRouterWaitsForHandoffThenCompletes(t *testing.T) {
	tp := &handoffTransport{fakeTransport: fakeTransport{eff: &answerEffects{}}, resumeToCaller: true}
	out, err := newRouter(tp).Ring(context.Background(), uuid.New().String()+"/call", ivrdrive.Target{Team: "sales"})
	if err != nil {
		t.Fatalf("Ring err: %v", err)
	}
	if !tp.waited {
		t.Fatal("AwaitHandoffEnd was not invoked after the agent answered")
	}
	if out != "completed" {
		t.Fatalf("outcome = %q, want completed", out)
	}
}

// When the caller leaves during the handoff, the transfer still reports
// "completed" (an agent answered) with ErrCallEnded, so the flow stops instead
// of playing a post-call node to an empty room.
func TestAgentRouterHandoffCallerGoneIsTerminal(t *testing.T) {
	tp := &handoffTransport{fakeTransport: fakeTransport{eff: &answerEffects{}}, resumeToCaller: false}
	out, err := newRouter(tp).Ring(context.Background(), uuid.New().String()+"/call", ivrdrive.Target{Team: "sales"})
	if !errors.Is(err, ivrdrive.ErrCallEnded) {
		t.Fatalf("Ring err = %v, want ErrCallEnded", err)
	}
	if out != "completed" {
		t.Fatalf("outcome = %q, want completed", out)
	}
}

// RingNoHandoff reports "completed" on an answer WITHOUT invoking the handoff wait,
// even when the transport implements HandoffWaiter (used by agent-initiated transfer,
// which must not resume any IVR flow).
func TestRingNoHandoffSkipsHandoffWait(t *testing.T) {
	tp := &handoffTransport{fakeTransport: fakeTransport{eff: &answerEffects{}}, resumeToCaller: true}
	got, err := newRouter(tp).RingNoHandoff(context.Background(), "11111111-1111-1111-1111-111111111111/room", ivrdrive.Target{Team: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "completed" {
		t.Fatalf("outcome = %q, want completed", got)
	}
	if tp.waited {
		t.Fatal("RingNoHandoff must not call AwaitHandoffEnd")
	}
}

// A target with no resolvable candidates is "no_answer".
func TestRingNoHandoffNoCandidates(t *testing.T) {
	tp := &fakeTransport{eff: &answerEffects{}}
	r := NewAgentRouter(fakeTeams{sips: nil}, routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil, nil), tp, nil)
	got, _ := r.RingNoHandoff(context.Background(), "11111111-1111-1111-1111-111111111111/room", ivrdrive.Target{Team: "sales"})
	if got != "no_answer" {
		t.Fatalf("outcome = %q, want no_answer", got)
	}
}

func TestAgentRouterRoutesTeamTransfer(t *testing.T) {
	tenant := uuid.New()
	eff := &answerEffects{}
	ar := NewAgentRouter(
		fakeTeams{sips: []string{"alice", "bob"}},
		routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil, nil),
		&fakeTransport{eff: eff},
		nil,
	)
	out, err := ar.Ring(context.Background(), tenant.String()+"/call", ivrdrive.Target{Team: "sales", Label: "Caller"})
	if err != nil {
		t.Fatalf("Ring err: %v", err)
	}
	if out != "completed" {
		t.Fatalf("outcome = %q, want completed", out)
	}
	if len(eff.got) == 0 {
		t.Fatalf("router was not driven with candidates")
	}
}
