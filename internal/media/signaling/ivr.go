package signaling

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/ivr"
	"github.com/whatomate/tring/internal/media/ivrdrive"
	"github.com/whatomate/tring/internal/media/prompt"
	"github.com/whatomate/tring/internal/media/room"
)

// ivrSession is a running IVR for one room: the prompt player injected as the
// IVR participant, the driver the walker drives, and a cancel to stop it when
// the caller leaves. done is closed once the walk goroutine has unwound and
// stored its path, so stopIVR can stamp the journey (incl. a transfer step the
// caller abandoned during) BEFORE the caller-left path settles the CDR.
type ivrSession struct {
	pid    room.ParticipantID
	player *prompt.Player
	driver *ivrdrive.Driver
	cancel context.CancelFunc
	done   chan struct{} // closed when the walk finished and path is set
	path   []ivr.Step    // the walked journey; read only after <-done
}

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
// per room — a second flow-pinned join is a no-op. The IVR's prompt player is
// injected via the SFU so the caller hears it; the caller's DTMF (routeDTMF)
// drives menus. callerRef is what a transfer ring shows the agent (a phone
// caller's number / a trusted client_ref; "" → generic). When the flow ends (or
// the caller leaves) the IVR is torn down.
func (s *Server) startIVR(roomID room.RoomID, flowID, callerRef string) {
	s.mu.Lock()
	if s.ivr[roomID] != nil {
		s.mu.Unlock()
		return // already running for this room
	}
	pid := room.ParticipantID("ivr-" + uuid.NewString())
	player := prompt.NewPlayer(pid, 0)
	ctx, cancel := context.WithCancel(context.Background())
	driver := ivrdrive.New(ivrdrive.Config{
		Prompts: &filePrompts{
			dir:       s.cfg.AudioDir,
			player:    player,
			waitFirst: func() { s.waitForConnected(roomID, ivrConnectWait) },
		},
		Agents:    s, // Server implements ivrdrive.Agents (rings agents into the room)
		Room:      string(roomID),
		ClientRef: callerRef,
		Logger:    s.log,
	})
	sess := &ivrSession{pid: pid, player: player, driver: driver, cancel: cancel, done: make(chan struct{})}
	s.ivr[roomID] = sess
	s.mu.Unlock()

	var (
		flow *ivr.Flow
		err  error
	)
	switch {
	case flowID == devFlowID && s.cfg.DevTokens:
		flow = devTransferFlow() // built-in: ring an agent, no DB needed
	case s.cfg.Flows != nil:
		flow, err = s.cfg.Flows.Load(ctx, flowID)
	default:
		err = errNoFlowLoader
	}
	if err != nil || flow == nil {
		s.log.Warn("ivr: flow load failed", "room", roomID, "flow", flowID, "err", err)
		close(sess.done) // no walk will run — unblock stopIVR's wait
		s.stopIVR(roomID)
		return
	}

	// Inject the IVR's voice into the room (existing peers subscribe to it).
	if err := s.sfu.InjectSource(roomID, player, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}); err != nil {
		s.log.Error("ivr: inject source failed", "room", roomID, "err", err)
		close(sess.done) // no walk will run — unblock stopIVR's wait
		s.stopIVR(roomID)
		return
	}

	s.log.Info("ivr started", "room", roomID, "flow", flowID, "entry", flow.EntryNode)
	go func() {
		path, walkErr := ivr.Walk(ctx, flow, flow.EntryNode, driver)
		s.log.Info("ivr flow ended", "room", roomID, "steps", len(path), "err", walkErr)
		sess.path = path
		close(sess.done)  // publish the path (happens-before stopIVR's read)
		s.stopIVR(roomID) // teardown: stamp the journey, remove the participant, close the player
	}()
}

// routeDTMF delivers a caller keypress to the room's IVR, if one is running.
func (s *Server) routeDTMF(roomID room.RoomID, digit string) {
	s.mu.Lock()
	sess := s.ivr[roomID]
	s.mu.Unlock()
	if sess != nil && digit != "" {
		sess.driver.DTMF(digit)
	}
}

// stopIVR tears down a room's IVR: cancels the walk, removes the injected
// participant, and closes the player. Idempotent.
func (s *Server) stopIVR(roomID room.RoomID) {
	s.mu.Lock()
	sess := s.ivr[roomID]
	delete(s.ivr, roomID)
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.cancel()
	// Wait for the walk to unwind so its journey is captured BEFORE the caller-left
	// path settles the CDR (which drops the active-call tracking Journey needs). The
	// walk returns near-instantly once ctx is cancelled; the cap just bounds a stuck
	// node. Only the goroutine that won the map delete reaches here, so Journey runs
	// once. This is what makes an abandoned call's transfer step persist.
	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
	}
	if s.cfg.Calls != nil {
		s.cfg.Calls.Journey(string(roomID), sess.path) // stamp the walk on the CDR
	}
	s.sfu.RemoveSource(roomID, sess.pid)
	_ = sess.player.Close()
	s.log.Info("ivr stopped", "room", roomID)
	// Flow ended with no agent → end the caller's call (else they're stranded in
	// an empty room). Web caller: terminal `ended` + remove. Phone caller: hang
	// up the leg. Both no-ops if an agent is present / the room is already empty.
	s.endIfAlone(roomID)
	s.hangupPstnIfAbandoned(roomID)
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
	// escape hatch). Strip the AudioDir prefix so both resolve, and Clean("/"+…)
	// confines the open to AudioDir (no traversal out).
	rel := strings.TrimPrefix(file, p.dir)
	f, err := os.Open(filepath.Join(p.dir, filepath.Clean("/"+rel)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	frames, err := prompt.LoadOggOpus(f)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.cache[file] = frames
	p.mu.Unlock()
	return frames, nil
}
