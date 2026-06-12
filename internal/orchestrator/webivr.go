package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/ivrdrive"
)

// webRingTimeout bounds how long a web transfer rings before no_answer.
const webRingTimeout = 30 * time.Second

// webDevFlowID runs the built-in dev transfer flow (no DB), matching the gateway
// dev flow so examples/rtc-test.html (flow_id="dev") works against the brain too.
const webDevFlowID = "dev"

var errNoWebFlows = errors.New("no web flow loader configured")

// WebFlowLoader loads an IVR flow by id for web/WhatsApp calls (internal/flowstore
// implements it; the orchestrator already loads flows for phone calls).
type WebFlowLoader interface {
	Load(ctx context.Context, flowID string) (*ivr.Flow, error)
}

// WebTeamResolver returns a team's member SIPs (internal/flowstore implements it).
// 2c: the BRAIN resolves teams + picks agents (reusing its routing); the edge no
// longer holds routing logic.
type WebTeamResolver interface {
	TeamSIPs(ctx context.Context, tenantID uuid.UUID, team string) ([]string, error)
}

// WebIVR runs the IVR for web/WhatsApp calls in the ORCHESTRATOR (the brain),
// driving media on the edge (gateway) over the control channel. It reuses the
// SAME ivrdrive.Driver the gateway used — only the Prompts/Agents backends differ
// (control-channel-backed here vs pion-local there). Phase 2b of
// docs/MEDIA_CONVERGENCE_PLAN.md: the walker lives with the brain; the edge only
// moves media.
type WebIVR struct {
	ctrl  *control.Server
	flows WebFlowLoader
	teams WebTeamResolver
	log   *slog.Logger

	mu       sync.Mutex
	sessions map[string]*webSession     // room → running IVR
	agents   map[string]map[string]bool // online web agents: tenant → sip → true
	rings    map[string]chan struct{}   // room → pending transfer ring (agent join closes it)
}

type webSession struct {
	driver *ivrdrive.Driver
	cancel context.CancelFunc
}

// NewWebIVR builds the brain-side web IVR. flows/teams may be nil (only the dev
// flow / ring-all then work).
func NewWebIVR(ctrl *control.Server, flows WebFlowLoader, teams WebTeamResolver, log *slog.Logger) *WebIVR {
	if log == nil {
		log = slog.Default()
	}
	return &WebIVR{
		ctrl:     ctrl,
		flows:    flows,
		teams:    teams,
		log:      log.With("component", "web-ivr"),
		sessions: map[string]*webSession{},
		agents:   map[string]map[string]bool{},
		rings:    map[string]chan struct{}{},
	}
}

// OnEvent handles control events from the edge. Wire it as the control server's
// event handler (control.Server.SetOnEvent).
func (w *WebIVR) OnEvent(m control.Message) {
	switch m.Type {
	case control.EvCallStarted:
		go w.start(m)
	case control.EvDTMF:
		w.routeDTMF(m)
	case control.EvCallEnded:
		w.stop(m.Room)
	case control.EvAgentOnline:
		w.agentPresence(m.Tenant, m.Sip, true)
	case control.EvAgentOffline:
		w.agentPresence(m.Tenant, m.Sip, false)
	case control.EvParticipantJoined:
		w.signalRing(m.Room) // an agent answering a pending transfer
	}
}

func (w *WebIVR) agentPresence(tenant, sip string, online bool) {
	if tenant == "" || sip == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if online {
		if w.agents[tenant] == nil {
			w.agents[tenant] = map[string]bool{}
		}
		w.agents[tenant][sip] = true
	} else if w.agents[tenant] != nil {
		delete(w.agents[tenant], sip)
	}
}

func (w *WebIVR) signalRing(room string) {
	w.mu.Lock()
	ch := w.rings[room]
	if ch != nil {
		delete(w.rings, room)
	}
	w.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// ring resolves the target to online agents (BRAIN-side routing), tells the edge
// to push `incoming` to each, and waits for one to join the room. Reuses the
// orchestrator's team resolution; the edge holds no routing logic.
func (w *WebIVR) ring(ctx context.Context, room string, t ivrdrive.Target) (string, error) {
	tenant, _, ok := strings.Cut(room, "/")
	if !ok {
		return "no_answer", nil
	}
	sips := w.resolveTargets(ctx, tenant, t)
	if len(sips) == 0 {
		return "no_answer", nil
	}

	ch := make(chan struct{})
	w.mu.Lock()
	w.rings[room] = ch
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.rings[room] == ch {
			delete(w.rings, room)
		}
		w.mu.Unlock()
	}()

	for _, sip := range sips {
		w.ctrl.Send(control.Message{Type: control.CmdRing, Room: room, Sip: sip, From: t.Label})
	}

	select {
	case <-ch:
		return "completed", nil
	case <-time.After(webRingTimeout):
		return "no_answer", nil
	case <-ctx.Done():
		return "no_answer", ctx.Err()
	}
}

// resolveTargets picks the online agent SIPs to ring: a specific agent, a team's
// online members, or all online agents of the tenant (ring-all).
func (w *WebIVR) resolveTargets(ctx context.Context, tenant string, t ivrdrive.Target) []string {
	w.mu.Lock()
	online := make(map[string]bool, len(w.agents[tenant]))
	for sip := range w.agents[tenant] {
		online[sip] = true
	}
	w.mu.Unlock()

	switch {
	case t.AgentSIP != "":
		if online[t.AgentSIP] {
			return []string{t.AgentSIP}
		}
		return nil
	case t.Team != "" && w.teams != nil:
		tid, err := uuid.Parse(tenant)
		if err != nil {
			return nil
		}
		members, err := w.teams.TeamSIPs(ctx, tid, t.Team)
		if err != nil {
			w.log.Warn("web ivr: team resolve failed", "team", t.Team, "err", err)
			return nil
		}
		var out []string
		for _, sip := range members {
			if online[sip] {
				out = append(out, sip)
			}
		}
		return out
	default:
		out := make([]string, 0, len(online))
		for sip := range online {
			out = append(out, sip)
		}
		return out
	}
}

func (w *WebIVR) start(m control.Message) {
	flow, err := w.loadFlow(m.FlowID)
	if err != nil || flow == nil {
		w.log.Warn("web ivr: flow load failed", "room", m.Room, "flow", m.FlowID, "err", err)
		return
	}
	driver := ivrdrive.New(ivrdrive.Config{
		Prompts:   &controlPrompts{ctrl: w.ctrl, room: m.Room},
		Agents:    &controlAgents{web: w},
		Room:      m.Room,
		ClientRef: m.ClientRef,
		Logger:    w.log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	if _, running := w.sessions[m.Room]; running {
		w.mu.Unlock()
		cancel()
		return // already running for this room
	}
	w.sessions[m.Room] = &webSession{driver: driver, cancel: cancel}
	w.mu.Unlock()

	w.log.Info("web ivr started", "room", m.Room, "flow", m.FlowID)
	if _, err := ivr.Walk(ctx, flow, flow.EntryNode, driver); err != nil {
		w.log.Warn("web ivr walk ended with error", "room", m.Room, "err", err)
	}
	// Flow ended → tell the edge to drop the IVR voice (best-effort).
	w.ctrl.Send(control.Message{Type: control.CmdIVRStop, Room: m.Room})
	w.stop(m.Room)
}

func (w *WebIVR) routeDTMF(m control.Message) {
	w.mu.Lock()
	s := w.sessions[m.Room]
	w.mu.Unlock()
	if s != nil {
		s.driver.DTMF(m.Digit)
	}
}

func (w *WebIVR) stop(room string) {
	w.mu.Lock()
	s := w.sessions[room]
	delete(w.sessions, room)
	w.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

func (w *WebIVR) loadFlow(flowID string) (*ivr.Flow, error) {
	if flowID == webDevFlowID {
		return webDevFlow(), nil
	}
	if w.flows == nil {
		return nil, errNoWebFlows
	}
	return w.flows.Load(context.Background(), flowID)
}

// webDevFlow is start → transfer (ring all online agents). Dev only — mirrors the
// gateway's built-in dev flow.
func webDevFlow() *ivr.Flow {
	return &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "x", Type: ivr.NodeTransfer, Config: map[string]any{}},
		},
		Edges: []ivr.Edge{{From: "s", To: "x", Condition: "default"}},
	}
}

// ── control-channel-backed ivrdrive backends ────────────────────────────

// controlPrompts plays a prompt by commanding the edge (which injects the audio
// into the room and reports done). Implements ivrdrive.Prompts.
type controlPrompts struct {
	ctrl *control.Server
	room string
}

func (p *controlPrompts) Play(ctx context.Context, file string) error {
	_, err := p.ctrl.Request(ctx, control.Message{Type: control.CmdPlay, Room: p.room, File: file})
	return err
}

// controlAgents resolves + rings agents on the BRAIN (2c): WebIVR.ring picks
// online agents (its routing/team resolution) and tells the edge to push
// `incoming` to each, then waits for a join. Implements ivrdrive.Agents.
type controlAgents struct {
	web *WebIVR
}

func (a *controlAgents) Ring(ctx context.Context, room string, t ivrdrive.Target) (string, error) {
	return a.web.ring(ctx, room, t)
}

var (
	_ ivrdrive.Prompts = (*controlPrompts)(nil)
	_ ivrdrive.Agents  = (*controlAgents)(nil)
)
