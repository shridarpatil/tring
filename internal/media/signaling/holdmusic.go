package signaling

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
)

// HoldMusicResolver resolves a tenant's configured system hold music
// (tenants.hold_music), "" when none is set (internal/flowstore implements it).
// Used by the agent-initiated hold path (msgHold); nil → agent hold plays
// silence. The queue/ring path gets its hold music from the team config instead.
type HoldMusicResolver interface {
	TenantHoldMusic(ctx context.Context, tenantID uuid.UUID) (string, error)
}

// holdFile returns the local Ogg/Opus file to play for a resolved hold-music
// value, or "" when nothing plays on the pion plane. The default MOH is the
// FreeSWITCH-only "local_stream://moh" stream sentinel (and a per-team override
// may be any URL) — pion has no stream player, only a file player, so any value
// carrying a scheme maps to silence. Empty (no hold music configured) is silence
// too. Net effect: visitors hear hold music only when a real file is configured.
func holdFile(moh string) string {
	if moh == "" || strings.Contains(moh, "://") {
		return ""
	}
	return moh
}

// playHold injects the tenant's hold music into the room as a looping,
// gapless-enough source and returns an idempotent stop func. moh is a resolved
// hold-music value (team MOH or tenant hold_music); when it isn't a playable
// local file (see holdFile) or fails to load, nothing is injected and stop is a
// no-op — the caller hears silence, exactly as before this existed.
//
// The injected source is an ordinary room publication, so every browser in the
// room (the waiting visitor, or the held party) hears it just like any other
// participant's audio — the same seam the IVR prompt player rides.
func (s *Server) playHold(roomID room.RoomID, moh string) func() {
	file := holdFile(moh)
	if file == "" {
		return func() {}
	}
	frames, err := loadOggFile(s.cfg.AudioDir, file)
	if err != nil {
		s.log.Warn("hold: load hold music failed", "room", roomID, "file", file, "err", err)
		return func() {}
	}

	pid := room.ParticipantID("hold-" + uuid.NewString())
	player := prompt.NewPlayer(pid, 0)
	if err := s.sfu.InjectSource(roomID, player, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}); err != nil {
		s.log.Warn("hold: inject hold music failed", "room", roomID, "err", err)
		_ = player.Close()
		return func() {}
	}
	// A phone leg forwards ONE feed; the held party's agent keeps sending (muted)
	// packets, so pin the music as the leg's feed until it's removed below.
	s.sfu.PinPortFeed(roomID, pid)
	s.log.Info("hold music started", "room", roomID, "file", file)

	stopCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			done := player.Play(frames) // re-queue the whole clip each pass = loop
			select {
			case <-stopCh:
				return
			case <-done:
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			s.sfu.RemoveSource(roomID, pid)
			_ = player.Close()
			s.log.Info("hold music stopped", "room", roomID)
		})
	}
}

// tenantHoldMusic resolves the tenant (from the "<tenant>/<name>" room id) and
// returns its configured system hold music, "" when unresolvable/unset/disabled.
func (s *Server) tenantHoldMusic(roomID room.RoomID) string {
	if s.cfg.HoldMusic == nil {
		return ""
	}
	tenant, _, _ := strings.Cut(string(roomID), "/")
	tid, err := uuid.Parse(tenant)
	if err != nil {
		return ""
	}
	moh, err := s.cfg.HoldMusic.TenantHoldMusic(context.Background(), tid)
	if err != nil {
		s.log.Warn("hold: resolve tenant hold music failed", "room", roomID, "err", err)
		return ""
	}
	return moh
}

// holdParty starts hold music for a room on an agent's msgHold (the agent mutes
// its own mic client-side, so the held party then hears only the music) and
// DEAFENS the holding agent: the music is an ordinary room publication, so
// without this the agent would hear both the music and the held party. Deafness
// is server-side, so it survives the agent refreshing the page mid-hold. A
// second hold while already held is a no-op.
//
// The room's hold slot is claimed BEFORE any side effect, so a second (or
// concurrent) hold returns without touching deafness or music — previously the
// losing hold tore down its own setup with SetDeaf(holder,false), un-deafening
// the agent while the winning hold's music kept playing.
func (s *Server) holdParty(roomID room.RoomID, holder room.ParticipantID) {
	h := &agentHold{}
	s.mu.Lock()
	if _, held := s.holds[roomID]; held {
		s.mu.Unlock()
		return
	}
	s.holds[roomID] = h.stop
	s.mu.Unlock()

	// Deafen BEFORE the music starts, so not one frame of it reaches the agent.
	s.setDeaf(roomID, holder, true)
	stopMusic := s.playHold(roomID, s.tenantHoldMusic(roomID))
	h.started(func() {
		stopMusic()
		s.setDeaf(roomID, holder, false)
	})
}

// agentHold is one room's hold. Its slot is claimed before the hold is set up,
// so a resume/teardown can arrive mid-setup: stop then just marks it, and
// started releases immediately.
type agentHold struct {
	mu      sync.Mutex
	stopped bool
	release func()
}

// started installs the hold's release func, running it at once if the hold was
// already stopped during setup.
func (h *agentHold) started(release func()) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		release()
		return
	}
	h.release = release
	h.mu.Unlock()
}

// stop releases the hold (idempotent).
func (h *agentHold) stop() {
	h.mu.Lock()
	h.stopped = true
	release := h.release
	h.release = nil
	h.mu.Unlock()
	if release != nil {
		release()
	}
}

// resumeParty stops a room's hold music and restores the holder's hearing on
// msgResume (idempotent).
func (s *Server) resumeParty(roomID room.RoomID) { s.clearHold(roomID) }

// clearHold stops and forgets a room's hold music if any is playing. Called on
// resume and on room teardown so a hold source never outlives its call.
func (s *Server) clearHold(roomID room.RoomID) {
	s.mu.Lock()
	stop := s.holds[roomID]
	delete(s.holds, roomID)
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// loadOggFile opens an Ogg/Opus file under dir and decodes it to per-frame Opus
// packets. file may be an absolute sound-library path (<dir>/<tenant>/<uuid>.ogg)
// or a relative custom path; either way the open is confined to dir (no
// traversal out). Shared by the IVR prompt loader and hold music.
func loadOggFile(dir, file string) ([][]byte, error) {
	rel := strings.TrimPrefix(file, dir)
	f, err := os.Open(filepath.Join(dir, filepath.Clean("/"+rel)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return prompt.LoadOggOpus(f)
}
