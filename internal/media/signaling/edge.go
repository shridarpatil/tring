package signaling

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/control"
	"github.com/whatomate/tring/internal/media/prompt"
	"github.com/whatomate/tring/internal/media/room"
)

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
			pid:     pid,
			prompts: &filePrompts{dir: s.cfg.AudioDir, player: prompt.NewPlayer(pid, 0)},
			ctx:     ctx,
			cancel:  cancel,
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
