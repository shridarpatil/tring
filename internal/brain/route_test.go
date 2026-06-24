package brain

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/whatomate/tring/internal/media/ivrdrive"
	"github.com/whatomate/tring/internal/routing"
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

func (f *fakeTransport) Prepare(_ context.Context, _ string, _ ivrdrive.Target, _ bool, sips []string, _ string) (func(context.Context) []string, routing.Effects, func()) {
	return func(context.Context) []string { return sips }, f.eff, func() {}
}

func TestAgentRouterRoutesTeamTransfer(t *testing.T) {
	tenant := uuid.New()
	eff := &answerEffects{}
	ar := NewAgentRouter(
		fakeTeams{sips: []string{"alice", "bob"}},
		routing.NewRouter(routing.NewMemQueue(), routing.NewMemReservations(), routing.NewMemRR(), nil, nil, nil),
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
