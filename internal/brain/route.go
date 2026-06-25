package brain

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/media/ivrdrive"
	"github.com/whatomate/tring/internal/queuekeys"
	"github.com/whatomate/tring/internal/routing"
)

// Teams resolves team membership + queue config (flowstore.Teams satisfies it).
type Teams interface {
	TeamSIPs(ctx context.Context, tenant uuid.UUID, team string) ([]string, error)
	TeamConfig(ctx context.Context, tenant uuid.UUID, team string) (routing.TeamConfig, bool, error)
}

// RingTransport is what a plug supplies so the brain can ring agents on it: per
// transfer, the live candidate source + the routing Effects (how to ring/hold/
// detect-answer on this channel) + a cleanup run after the loop. The brain owns
// team resolution + the routing.Router queue/ring loop; the plug owns the wires.
// strategy is the resolved team strategy (e.g. "round_robin") so the transport
// can drive one-at-a-time vs ring-all behaviour inside its Effects adapter.
type RingTransport interface {
	Prepare(ctx context.Context, roomID string, t ivrdrive.Target, ringAll bool, sips []string, strategy string) (candidates func(context.Context) []string, eff routing.Effects, cleanup func())
}

// AgentRouter is the brain-owned implementation of ivrdrive.Agents: it sequences
// team resolution → RouteConfig → routing.Router.Run, transport-agnostically.
type AgentRouter struct {
	teams  Teams
	router *routing.Router
	tport  RingTransport
	log    *slog.Logger
}

func NewAgentRouter(teams Teams, router *routing.Router, tport RingTransport, log *slog.Logger) *AgentRouter {
	if log == nil {
		log = slog.Default()
	}
	return &AgentRouter{teams: teams, router: router, tport: tport, log: log.With("component", "brain.route")}
}

// Ring satisfies ivrdrive.Agents. roomID is the tenant-namespaced "<tenant>/<name>".
func (a *AgentRouter) Ring(ctx context.Context, roomID string, t ivrdrive.Target) (string, error) {
	tenant, _, ok := strings.Cut(roomID, "/")
	if !ok {
		a.log.Debug("route: malformed roomID", "room", roomID)
		return "no_answer", nil
	}

	var sips []string
	ringAll := true
	if t.AgentSIP != "" {
		sips, ringAll = []string{t.AgentSIP}, false
	} else if t.Team != "" && a.teams != nil {
		ringAll = false
		if tid, err := uuid.Parse(tenant); err == nil {
			if m, err := a.teams.TeamSIPs(ctx, tid, t.Team); err == nil {
				sips = m
			} else {
				a.log.Warn("transfer: team resolve failed", "team", t.Team, "err", err)
			}
		}
	}
	if !ringAll && len(sips) == 0 {
		return "no_answer", nil
	}

	rc := routing.RouteConfig{TenantID: tenant}
	if !ringAll && t.Team != "" && a.teams != nil {
		// TODO(plug): make per-plug when a 2nd plug lands
		rc.Team, rc.Source = t.Team, queuekeys.SourceWeb
		if tid, err := uuid.Parse(tenant); err == nil {
			if tc, ok, _ := a.teams.TeamConfig(ctx, tid, t.Team); ok {
				rc.Strategy = tc.Strategy
				rc.RingSeconds = time.Duration(tc.RingSeconds) * time.Second
				rc.MaxWait = time.Duration(tc.MaxWait) * time.Second
			}
		}
	}

	candidates, eff, cleanup := a.tport.Prepare(ctx, roomID, t, ringAll, sips, rc.Strategy)
	outcome, _, _ := a.router.Run(ctx, rc, candidates, eff)
	if cleanup != nil {
		cleanup()
	}
	if outcome == routing.OutcomeCompleted {
		return "completed", nil
	}
	return "no_answer", nil
}
