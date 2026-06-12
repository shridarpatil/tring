package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/ivrdrive"
)

// webDevFlowID runs the built-in dev transfer flow (no DB), matching the gateway
// dev flow so examples/rtc-test.html (flow_id="dev") works against the brain too.
const webDevFlowID = "dev"

var errNoWebFlows = errors.New("no web flow loader configured")

// WebFlowLoader loads an IVR flow by id for web/WhatsApp calls (internal/flowstore
// implements it; the orchestrator already loads flows for phone calls).
type WebFlowLoader interface {
	Load(ctx context.Context, flowID string) (*ivr.Flow, error)
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
	log   *slog.Logger

	mu       sync.Mutex
	sessions map[string]*webSession // room → running IVR
}

type webSession struct {
	driver *ivrdrive.Driver
	cancel context.CancelFunc
}

// NewWebIVR builds the brain-side web IVR. flows may be nil (only the dev flow
// then works).
func NewWebIVR(ctrl *control.Server, flows WebFlowLoader, log *slog.Logger) *WebIVR {
	if log == nil {
		log = slog.Default()
	}
	return &WebIVR{
		ctrl:     ctrl,
		flows:    flows,
		log:      log.With("component", "web-ivr"),
		sessions: map[string]*webSession{},
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
		Agents:    &controlAgents{ctrl: w.ctrl, room: m.Room},
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

// controlAgents rings an agent by commanding the edge (2b: the edge still holds
// the agent registry; 2c moves the ring resolution into the brain). Implements
// ivrdrive.Agents.
type controlAgents struct {
	ctrl *control.Server
	room string
}

func (a *controlAgents) Ring(ctx context.Context, room string, t ivrdrive.Target) (string, error) {
	resp, err := a.ctrl.Request(ctx, control.Message{
		Type: control.CmdRing, Room: room, Team: t.Team, Sip: t.AgentSIP, From: t.Label,
	})
	if err != nil {
		return "", err
	}
	return resp.Outcome, nil
}

var (
	_ ivrdrive.Prompts = (*controlPrompts)(nil)
	_ ivrdrive.Agents  = (*controlAgents)(nil)
)
