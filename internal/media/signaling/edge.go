package signaling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/prompt"
	"github.com/whatomate/tring/internal/media/room"
)

// errTokenReused is returned when a visitor token's jti was already consumed.
var errTokenReused = errors.New("visitor token already used")

// consumeVisitorToken burns a visitor token's jti single-use via the token store
// (Redis), mirroring the gateway. Fails CLOSED: no jti, expired, no store, or a
// store error all reject — a captured token can't be amplified into many calls.
func (s *Server) consumeVisitorToken(ctx context.Context, jti string, exp time.Time) error {
	if jti == "" {
		return errors.New("visitor token has no jti")
	}
	ttl := time.Until(exp)
	if ttl <= 0 {
		return errors.New("visitor token expired")
	}
	if s.cfg.Tokens == nil {
		return errors.New("single-use store unavailable")
	}
	fresh, err := s.cfg.Tokens.SetNX(ctx, "rtctoken:"+jti, []byte("1"), ttl)
	if err != nil {
		return fmt.Errorf("single-use check failed: %w", err)
	}
	if !fresh {
		return errTokenReused
	}
	return nil
}

// controlChannel is the edge side of the brain↔edge link (control.Client
// satisfies it): the edge sends events/responses up and knows if it's connected.
type controlChannel interface {
	Send(control.Message) error
	Connected() bool
}

// SetControl attaches the control channel. With it connected, flow-pinned calls
// are driven by the orchestrator (the brain) over the channel instead of the
// local walker. Call once at startup (cmd/gateway).
func (s *Server) SetControl(c controlChannel) { s.control = c }

// controlUp reports whether the brain is reachable right now (else the edge
// falls back to running the IVR locally).
func (s *Server) controlUp() bool { return s.control != nil && s.control.Connected() }

// edgeVoice is the brain-driven IVR voice for one room: a prompt player injected
// as a room participant that the brain commands (`play`) and tears down (`ivr.stop`).
type edgeVoice struct {
	pid     room.ParticipantID
	prompts *filePrompts
	cancel  context.CancelFunc
	ctx     context.Context
}

// HandleControlCommand executes a command from the brain against this edge's
// media. Wire it as the control client's onCommand (cmd/gateway). Commands that
// carry an ID get a correlated response so the brain's Request can resolve.
func (s *Server) HandleControlCommand(m control.Message) {
	switch m.Type {
	case control.CmdPlay:
		err := s.edgePlay(room.RoomID(m.Room), m.File)
		resp := control.Message{ID: m.ID, OK: err == nil}
		if err != nil {
			// Surface a bad/missing prompt loudly — the #1 cause of a "silent IVR"
			// is a non-Opus file (the player decodes Ogg/Opus only, not WAV/MP3).
			s.log.Warn("ivr prompt play failed (needs Ogg/Opus)", "room", m.Room, "file", m.File, "err", err)
			resp.Reason = err.Error()
		}
		_ = s.control.Send(resp)
	case control.CmdIVRStop:
		s.edgeStopVoice(room.RoomID(m.Room))
	case control.CmdRing:
		// 2c: the brain already resolved WHICH agent — the edge just pushes the
		// incoming ring to that agent's socket (no routing here). The brain learns
		// the agent answered via the participant.joined event, so no response.
		s.pushIncoming(m.Room, m.Sip, m.From)
	case control.CmdRemove:
		s.removeParticipant(room.RoomID(m.Room), room.ParticipantID(m.Participant))
	case control.CmdHold:
		s.edgeHold(room.RoomID(m.Room))
	case control.CmdResume:
		s.edgeResume(room.RoomID(m.Room))
	default:
		s.log.Warn("control: unknown command", "type", m.Type)
	}
}

// edgePlay injects the room's IVR voice (once) and plays a prompt, blocking until
// it finishes — so the brain's Play request resolves on completion.
func (s *Server) edgePlay(roomID room.RoomID, file string) error {
	s.mu.Lock()
	ev := s.edge[roomID]
	fresh := ev == nil
	if fresh {
		pid := room.ParticipantID("ivr-" + uuid.NewString())
		ctx, cancel := context.WithCancel(context.Background())
		ev = &edgeVoice{
			pid: pid,
			prompts: &filePrompts{
				dir:       s.cfg.AudioDir,
				player:    prompt.NewPlayer(pid, 0),
				waitFirst: func() { s.waitForConnected(roomID, ivrConnectWait) },
			},
			ctx:    ctx,
			cancel: cancel,
		}
		s.edge[roomID] = ev
	}
	s.mu.Unlock()

	if fresh {
		if err := s.sfu.InjectSource(roomID, ev.prompts.player, webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		}); err != nil {
			s.edgeStopVoice(roomID)
			return err
		}
		s.log.Info("edge ivr voice injected", "room", roomID)
	}
	if file == "" {
		return nil // a node with no audio still "plays" instantly
	}
	return ev.prompts.Play(ev.ctx, file)
}

// pushIncoming rings one agent: it sends the `incoming` ring to that agent's
// presence socket. The room id is tenant-namespaced ("<tenant>/<name>"); the
// agent joins the un-namespaced name (its own tenant re-namespaces it).
func (s *Server) pushIncoming(roomID, sip, from string) {
	tenant, name, ok := strings.Cut(roomID, "/")
	if !ok {
		return
	}
	s.mu.Lock()
	c := s.agents[tenant][sip]
	s.mu.Unlock()
	if c != nil {
		_ = c.send(msg{Type: msgIncoming, Room: name, From: from})
	}
}

// waitForConnected blocks (up to d) until a participant in the room has connected
// media, so the FIRST IVR prompt isn't played into a not-yet-connected path and
// missed (and the menu's input window doesn't start counting before the caller
// can hear it). Best-effort: returns after the timeout regardless.
func (s *Server) waitForConnected(roomID room.RoomID, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r, ok := s.rooms.Get(roomID); ok {
			for _, p := range r.Participants() {
				if p.State() == room.StateConnected {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ivrConnectWait bounds how long the first prompt waits for the caller's media.
const ivrConnectWait = 6 * time.Second

// holdMusicFile is the MOH prompt the edge loops on hold (from AudioDir). A
// missing file → silent hold (the loop just exits). Tenant-specific hold music
// is a later refinement (the brain would pass the file in CmdHold).
const holdMusicFile = "hold.ogg"

// removeParticipant drops a leg from the room (e.g. an agent who transferred the
// call out): it leaves the room (peers get peer-left) and its socket is closed,
// so the client sees the call end.
func (s *Server) removeParticipant(roomID room.RoomID, pid room.ParticipantID) {
	if r, ok := s.rooms.Get(roomID); ok {
		for _, p := range r.Participants() {
			if p.ID() == pid {
				p.Leave()
				break
			}
		}
	}
	s.mu.Lock()
	c := s.conns[pid]
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// edgeHold injects looping hold music into the room (the other party hears MOH).
// Idempotent. The transferring/holding agent's own audio is muted client-side.
func (s *Server) edgeHold(roomID room.RoomID) {
	s.mu.Lock()
	if s.holds[roomID] != nil {
		s.mu.Unlock()
		return
	}
	pid := room.ParticipantID("moh-" + uuid.NewString())
	ctx, cancel := context.WithCancel(context.Background())
	hv := &edgeVoice{
		pid:     pid,
		prompts: &filePrompts{dir: s.cfg.AudioDir, player: prompt.NewPlayer(pid, 0)},
		ctx:     ctx,
		cancel:  cancel,
	}
	s.holds[roomID] = hv
	s.mu.Unlock()

	if err := s.sfu.InjectSource(roomID, hv.prompts.player, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}); err != nil {
		s.edgeResume(roomID)
		return
	}
	go func() {
		for ctx.Err() == nil {
			if err := hv.prompts.Play(ctx, holdMusicFile); err != nil {
				return // missing file / cancelled → silent hold
			}
		}
	}()
}

// edgeResume stops a room's hold music. Idempotent.
func (s *Server) edgeResume(roomID room.RoomID) {
	s.mu.Lock()
	hv := s.holds[roomID]
	delete(s.holds, roomID)
	s.mu.Unlock()
	if hv == nil {
		return
	}
	hv.cancel()
	s.sfu.RemoveSource(roomID, hv.pid)
	_ = hv.prompts.player.Close()
}

// edgeStopVoice removes a room's brain-driven IVR voice. Idempotent.
func (s *Server) edgeStopVoice(roomID room.RoomID) {
	s.mu.Lock()
	ev := s.edge[roomID]
	delete(s.edge, roomID)
	s.mu.Unlock()
	if ev == nil {
		return
	}
	ev.cancel()
	s.sfu.RemoveSource(roomID, ev.pid)
	_ = ev.prompts.player.Close()
	s.log.Info("edge ivr voice stopped", "room", roomID)
}
