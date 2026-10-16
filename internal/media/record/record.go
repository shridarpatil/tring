// Package record captures a call's media to disk: each participant's Opus RTP is
// written losslessly to its own Ogg/Opus file (no transcode), and the per-leg
// files are merged into one stereo recording (agent left, customer right) at
// finalize (MergeStereo).
//
// A Recorder implements room.TrackSink. Attach it to the participants to record
// via SFU.AddSink(room, pid, rec); it demuxes by participant id. This is the ONE
// recorder for every call type (web / WhatsApp / phone-as-pion-leg) — the
// orchestrator owns when to start/stop and the resulting CDR reference. See
// docs/src/content/docs/architecture/overview.md (Phase 1).
package record

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"

	"github.com/shridarpatil/tring/internal/media/room"
)

// opusRate / opusChannels are the WebRTC Opus parameters the SFU forwards. They
// go in the Ogg/Opus header; the RTP payload is written untouched, so the file
// is a lossless capture of exactly what flowed (no decode/re-encode).
const (
	opusRate     = 48000
	opusChannels = 2
)

// Track is one recorded participant leg.
type Track struct {
	PID    room.ParticipantID
	Role   string        // "agent" / "customer" / … — drives the stereo channel mapping
	Path   string        // <dir>/<callID>-<pid>.ogg (unique per participant, not per role)
	Offset time.Duration // when this leg's first packet landed, relative to Arm — so a
	// later-joining agent (transfer) is placed at its real position on the customer
	// timeline instead of at t=0. ~0 for the caller and the first agent.
}

// Recorder writes each participant's Opus RTP to its own Ogg file. Its WriteRTP
// is safe to call from the SFU copy loops (one goroutine per publisher).
type Recorder struct {
	dir    string
	callID string

	mu      sync.Mutex
	roles   map[room.ParticipantID]string
	writers map[room.ParticipantID]*oggwriter.OggWriter
	paths   map[room.ParticipantID]string
	offsets map[room.ParticipantID]time.Duration // first-packet offset from armedAt
	armed   bool
	armedAt time.Time
	closed  bool
}

// New builds a recorder that writes <dir>/<callID>-<pid>.ogg per participant.
func New(dir, callID string) *Recorder {
	return &Recorder{
		dir:     dir,
		callID:  callID,
		roles:   map[room.ParticipantID]string{},
		writers: map[room.ParticipantID]*oggwriter.OggWriter{},
		paths:   map[room.ParticipantID]string{},
		offsets: map[room.ParticipantID]time.Duration{},
	}
}

// Label sets a participant's role (used for the file name + stereo mapping). Set
// it before media flows; an unlabelled participant falls back to its id.
func (r *Recorder) Label(pid room.ParticipantID, role string) {
	r.mu.Lock()
	r.roles[pid] = role
	r.mu.Unlock()
}

// Arm starts capture. Until it's called, WriteRTP drops every packet, so a
// recorder attached to the caller's leg at call start records NOTHING during the
// pre-answer phase (IVR menus, ringing, queue hold). callcdr arms the recorder
// when an agent answers — so the recording begins at answer, and both legs share
// one timeline. Idempotent.
func (r *Recorder) Arm() {
	r.mu.Lock()
	if !r.armed {
		r.armed = true
		r.armedAt = time.Now()
	}
	r.mu.Unlock()
}

// WriteRTP implements room.TrackSink: it lazily opens the participant's Ogg
// writer and appends the packet. Packets before Arm (pre-answer) and after Close
// are dropped.
func (r *Recorder) WriteRTP(pid room.ParticipantID, pkt *rtp.Packet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.armed {
		return nil
	}
	w := r.writers[pid]
	if w == nil {
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(r.dir, r.fileName(pid))
		nw, err := oggwriter.New(path, opusRate, opusChannels)
		if err != nil {
			return err
		}
		w, r.writers[pid], r.paths[pid] = nw, nw, path
		// Record where this leg begins relative to the recording start, so a leg that
		// joins mid-call (an agent answering a transfer) is placed at its real time on
		// the merged timeline. Clamp negative skew to 0.
		if off := time.Since(r.armedAt); off > 0 {
			r.offsets[pid] = off
		}
	}
	return w.WriteRTP(pkt)
}

// Close flushes and closes every participant writer. Idempotent.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var firstErr error
	for _, w := range r.writers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Tracks returns the recorded legs (after writes have started). Order is
// unspecified; callers select by Role.
func (r *Recorder) Tracks() []Track {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Track, 0, len(r.paths))
	for pid, path := range r.paths {
		out = append(out, Track{PID: pid, Role: r.roleOf(pid), Path: path, Offset: r.offsets[pid]})
	}
	return out
}

// fileName keys the per-leg file on the PARTICIPANT id, not the role: a call that
// passes through more than one agent (a blind transfer) has two legs with role
// "agent", and a role-keyed name would make the second overwrite the first — the
// bug that left only the last agent in the recording. The role still drives the
// stereo channel mapping (via Track.Role), it just no longer names the file.
func (r *Recorder) fileName(pid room.ParticipantID) string {
	return fmt.Sprintf("%s-%s.ogg", r.callID, sanitize(string(pid)))
}

// roleOf must be called with r.mu held.
func (r *Recorder) roleOf(pid room.ParticipantID) string {
	if role := r.roles[pid]; role != "" {
		return role
	}
	return string(pid)
}

func sanitize(s string) string {
	return strings.Map(func(rn rune) rune {
		switch {
		case rn >= 'a' && rn <= 'z', rn >= 'A' && rn <= 'Z', rn >= '0' && rn <= '9', rn == '-', rn == '_':
			return rn
		default:
			return '_'
		}
	}, s)
}

var _ room.TrackSink = (*Recorder)(nil)
