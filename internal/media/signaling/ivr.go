package signaling

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/media/ivrdrive"
	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
)

// devFlowID is the magic flow id that, with dev tokens on, runs the built-in
// dev transfer flow (no DB needed) so the example clients demo a call→agent ring.
const devFlowID = "dev"

// errNoFlowLoader is logged when a flow is pinned but no loader is configured.
var errNoFlowLoader = errors.New("no flow loader configured")

// devTransferFlow is start → transfer (ring all online agents). Dev only.
func devTransferFlow() *ivr.Flow {
	return &ivr.Flow{
		EntryNode: "s",
		Nodes: []ivr.Node{
			{ID: "s", Type: ivr.NodeStart},
			{ID: "x", Type: ivr.NodeTransfer, Config: map[string]any{}}, // no team/agent → ring all online
		},
		Edges: []ivr.Edge{{From: "s", To: "x", Condition: "default"}},
	}
}

// startIVR loads the pinned flow and runs it as a participant in the room. Once
// per room — a second flow-pinned join is a no-op (brain.Runner enforces this).
// The IVR's prompt player is injected via the SFU so the caller hears it; the
// caller's DTMF (routeDTMF) drives menus. callerRef is what a transfer ring shows
// the agent (a phone caller's number / a trusted client_ref; "" → generic). When
// the flow ends (or the caller leaves) the IVR is torn down.
func (s *Server) startIVR(roomID room.RoomID, flowID, callerRef string) {
	if s.brain.Running(roomID) {
		return // an IVR is already running for this room
	}

	pid := room.ParticipantID("ivr-" + uuid.NewString())
	player := prompt.NewPlayer(pid, 0)
	driver := ivrdrive.New(ivrdrive.Config{
		Prompts: &filePrompts{
			dir:       s.cfg.AudioDir,
			player:    player,
			waitFirst: func() { s.waitForConnected(roomID, ivrConnectWait) },
		},
		Agents: s.agentRouter, // brain.AgentRouter drives team-resolution + routing loop
		Dialer: ivrDialer{s},  // transfer-to-number (ivrdial.go)
		// While a dialed number rings, the caller hears the tenant's hold music
		// (silence when it isn't a playable file — see holdFile).
		DialAudio: func() string { return holdFile(s.tenantHoldMusic(roomID)) },
		Room:      string(roomID),
		ClientRef: callerRef,
		Logger:    s.log,
	})

	var (
		flow *ivr.Flow
		err  error
	)
	switch {
	case flowID == devFlowID && s.cfg.DevTokens:
		flow = devTransferFlow() // built-in: ring an agent, no DB needed
	case s.cfg.Flows != nil:
		flow, err = s.cfg.Flows.Load(context.Background(), flowID)
	default:
		err = errNoFlowLoader
	}
	if err != nil || flow == nil {
		s.log.Warn("ivr: flow load failed", "room", roomID, "flow", flowID, "err", err)
		_ = player.Close()
		return
	}

	onJourney := func(path []ivr.Step) {
		if s.cfg.Calls != nil {
			s.cfg.Calls.Journey(string(roomID), path)
		}
	}
	onDone := func() {
		s.sfu.RemoveSource(roomID, pid)
		_ = player.Close()
		s.clearCaller(roomID) // drop caller/handoff tracking (handoff.go)
		s.log.Info("ivr stopped", "room", roomID)
		// Flow ended with no agent → end the caller's call (else they're stranded in
		// an empty room). Web caller: terminal `ended` + remove. Phone caller: hang
		// up the leg. Both no-ops if an agent is present / the room is already empty.
		s.endIfAlone(roomID)
		s.hangupPstnIfAbandoned(roomID)
	}

	// Inject the IVR's voice into the room as the setup step. By passing this
	// inside setup, brain.Runner claims the room slot first — so a concurrent
	// startIVR that also races past the Running() check above will lose the slot
	// race and never inject, preventing a leaked orphaned player/source.
	setup := func() error {
		return s.sfu.InjectSource(roomID, player, webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		})
	}
	s.log.Info("ivr started", "room", roomID, "flow", flowID, "entry", flow.EntryNode)
	if !s.brain.Start(context.Background(), roomID, flow, driver, setup, onJourney, onDone) {
		_ = player.Close() // lost the race or setup failed — nothing injected for us to remove
		return
	}
}

// routeDTMF delivers a caller keypress to the room's IVR, if one is running.
func (s *Server) routeDTMF(roomID room.RoomID, digit string) {
	s.brain.DTMF(roomID, digit)
}

// stopIVR tears down a room's IVR. Delegates to brain.Runner which cancels the
// walk, waits for the path (stamps the journey BEFORE onDone fires), then runs
// teardown. Idempotent.
func (s *Server) stopIVR(roomID room.RoomID) {
	s.brain.Stop(roomID)
}

// filePrompts plays Ogg/Opus prompt files (from AudioDir) through the IVR's
// player, caching decoded frames so a repeated prompt isn't re-read.
type filePrompts struct {
	dir    string
	player *prompt.Player
	// waitFirst runs once before the first prompt (used to wait for the caller's
	// media to connect). nil → no wait. Gated here so prompt-less flows (a bare
	// transfer) never block.
	waitFirst func()
	once      sync.Once

	mu    sync.Mutex
	cache map[string][][]byte
}

// Play streams the named prompt file and blocks until it finishes (or ctx ends).
func (p *filePrompts) Play(ctx context.Context, file string) error {
	if p.waitFirst != nil {
		p.once.Do(p.waitFirst)
	}
	frames, err := p.load(file)
	if err != nil {
		return err
	}
	done := p.player.Play(frames)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop halts the prompt currently playing (IVR barge-in): it flushes the player's
// queued audio so a Play in flight returns at once. Safe to call when nothing is
// playing. Leaves the player usable for the next prompt (unlike Close).
func (p *filePrompts) Stop() { p.player.Flush() }

func (p *filePrompts) load(file string) ([][]byte, error) {
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string][][]byte{}
	}
	if frames, ok := p.cache[file]; ok {
		p.mu.Unlock()
		return frames, nil
	}
	p.mu.Unlock()

	// audio_file may be an absolute file_path from the sound library
	// (<AudioDir>/<tenant>/<uuid>.ogg) or a relative path (the custom-path
	// escape hatch); loadOggFile handles both and confines the open to AudioDir.
	frames, err := loadOggFile(p.dir, file)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.cache[file] = frames
	p.mu.Unlock()
	return frames, nil
}
