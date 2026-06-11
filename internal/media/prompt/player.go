// Package prompt is the IVR's voice on the pion media plane: a room.Participant
// Source that streams Opus frames (a greeting, menu prompt, or hold music) into
// a room as paced RTP. It's injected via SFU.InjectSource, so the caller hears
// it exactly like another participant — the foundation for the IVR participant
// (internal/media/ivrdrive) and, later, an AI voice agent's TTS output.
package prompt

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"

	"github.com/whatomate/tring/internal/media/room"
)

// opusFrameSamples is the RTP timestamp increment per 20ms Opus frame at 48kHz.
const opusFrameSamples = 960

// errClosed ends the SFU copy loop when the player is closed.
var errClosed = errors.New("prompt: player closed")

// Player is a room.ParticipantSource that plays queued Opus frames as RTP, one
// 20ms frame per packet, paced in real time. Between prompts ReadRTP blocks
// (the participant is silent) until the next Play or Close.
type Player struct {
	id       room.ParticipantID
	frameDur time.Duration

	mu     sync.Mutex
	cond   *sync.Cond
	frames [][]byte
	done   chan struct{} // closed when the current batch fully drains
	closed bool

	seq      uint16
	ts       uint32
	lastSent time.Time
}

// NewPlayer builds a player for participant id. frameDur is the inter-packet
// pacing (0 → 20ms); pass a small value in tests to avoid real-time waits.
func NewPlayer(id room.ParticipantID, frameDur time.Duration) *Player {
	if frameDur <= 0 {
		frameDur = 20 * time.Millisecond
	}
	p := &Player{id: id, frameDur: frameDur}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// ID implements room.ParticipantSource.
func (p *Player) ID() room.ParticipantID { return p.id }

// Play enqueues frames and returns a channel closed once they've all been read
// out (playback finished). Frames already queued play first. A nil/empty batch
// returns an already-closed channel.
func (p *Player) Play(frames [][]byte) <-chan struct{} {
	done := make(chan struct{})
	if len(frames) == 0 {
		close(done)
		return done
	}
	p.mu.Lock()
	p.frames = append(p.frames, frames...)
	p.done = done
	p.mu.Unlock()
	p.cond.Signal()
	return done
}

// ReadRTP implements room.ParticipantSource: it returns the next queued frame as
// an RTP packet, blocking (silent) when idle and pacing packets frameDur apart.
func (p *Player) ReadRTP() (*rtp.Packet, error) {
	p.mu.Lock()
	for len(p.frames) == 0 && !p.closed {
		p.cond.Wait()
	}
	if p.closed {
		p.mu.Unlock()
		return nil, errClosed
	}
	frame := p.frames[0]
	p.frames = p.frames[1:]
	drained := len(p.frames) == 0
	done := p.done
	seq, ts := p.seq, p.ts
	p.seq++
	p.ts += opusFrameSamples
	wait := p.pacingLocked()
	if drained {
		p.done = nil
	}
	p.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
	p.mu.Lock()
	p.lastSent = time.Now()
	p.mu.Unlock()

	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: ts},
		Payload: frame,
	}
	if drained && done != nil {
		close(done) // playback of this batch finished
	}
	return pkt, nil
}

// pacingLocked returns how long to wait before sending the next packet to keep
// ~frameDur cadence. Caller holds mu.
func (p *Player) pacingLocked() time.Duration {
	if p.lastSent.IsZero() {
		return 0
	}
	elapsed := time.Since(p.lastSent)
	if elapsed >= p.frameDur {
		return 0
	}
	return p.frameDur - elapsed
}

// Close ends playback and unblocks ReadRTP (the SFU copy loop then exits).
func (p *Player) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
	p.mu.Unlock()
	p.cond.Broadcast()
	return nil
}

// LoadOggOpus reads all Opus audio frames from an Ogg/Opus stream (the format
// Tring records/stores prompts in). The OpusHead page is consumed by the reader
// and the OpusTags comment page is skipped, so only audio frames are returned.
func LoadOggOpus(r io.Reader) ([][]byte, error) {
	og, _, err := oggreader.NewWith(r)
	if err != nil {
		return nil, err
	}
	var frames [][]byte
	skippedTags := false
	for {
		payload, _, err := og.ParseNextPage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !skippedTags {
			skippedTags = true // first page after OpusHead is the OpusTags comment
			continue
		}
		if len(payload) > 0 {
			frames = append(frames, payload)
		}
	}
	return frames, nil
}
