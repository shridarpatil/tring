package sfu

import (
	"github.com/shridarpatil/tring/internal/media/g711"
	"github.com/shridarpatil/tring/internal/media/room"
)

// Mixing for ports. A port normally forwards one publisher at a time (see
// roomPort), which is enough for caller ↔ IVR and caller ↔ agent. A supervisor
// barging into a phone call needs the caller to hear the agent AND the
// supervisor, so while a barge is on the port decodes every publisher it hears,
// sums them each 20ms and re-encodes one stream in the room's codec.
//
// G.711 (phone rooms) is coded in pure Go (internal/media/g711). Opus needs
// libopus (cgo):
// mix_opus.go, built with `-tags opus`, sets newOpusDecoder/newOpusEncoder;
// without the tag they stay nil, the binary stays cgo-free, and a barge on an
// Opus phone leg falls back to one speaker at a time.

// opusDecoder / opusEncoder are the codec parts the mixer uses, so the mixing
// logic is testable without cgo. Both work on mono int16 PCM at the codec's
// rate (48kHz Opus, 8kHz G.711).
type opusDecoder interface {
	Decode(data []byte, pcm []int16) (int, error)
}

type opusEncoder interface {
	Encode(pcm []int16, data []byte) (int, error)
}

var (
	newOpusDecoder func() (opusDecoder, error)
	newOpusEncoder func() (opusEncoder, error)
)

const (
	// mixFrame is one 20ms frame of 48kHz mono PCM, the Opus mixer's tick.
	mixFrame = 960
	// mixPrimeFrames is how much a source buffers before it's mixed (and again
	// after it runs dry): 40ms absorbs network jitter at the cost of 40ms of
	// latency.
	mixPrimeFrames = 2
	// mixMaxFrames caps a source's backlog; a sender running fast loses its
	// oldest audio rather than drifting ever later.
	mixMaxFrames = 5
	// maxOpusFrame is the largest Opus frame (120ms at 48kHz), the decode
	// scratch size (a G.711 packet is far smaller).
	maxOpusFrame = 6 * mixFrame
)

// mixSource is one publisher's decoder and its decoded, not yet mixed audio.
type mixSource struct {
	dec    opusDecoder
	buf    []int16
	primed bool
}

// portMixer turns several publishers' packets into one stream in the leg's codec. Not
// safe for concurrent use: the port calls it under its mutex.
type portMixer struct {
	tick   int // samples per 20ms tick at the codec's rate
	newDec func() (opusDecoder, error)
	enc    opusEncoder
	srcs   map[room.ParticipantID]*mixSource
	pcm    []int16 // decode scratch
	sum    []int16 // mixed frame scratch
	out    []byte  // encode scratch
}

// newPortMixer builds a mixer for a leg in codec c, or returns nil when it
// can't code c (Opus in a binary without libopus).
func newPortMixer(c room.Codec) (*portMixer, error) {
	newDec, newEnc := newOpusDecoder, newOpusEncoder
	if c.G711() {
		g := g711Coder{c}
		newDec = func() (opusDecoder, error) { return g, nil }
		newEnc = func() (opusEncoder, error) { return g, nil }
	}
	if newEnc == nil || newDec == nil {
		return nil, nil
	}
	enc, err := newEnc()
	if err != nil {
		return nil, err
	}
	frame := int(c.FrameSamples())
	return &portMixer{
		tick:   frame,
		newDec: newDec,
		enc:    enc,
		srcs:   make(map[room.ParticipantID]*mixSource),
		pcm:    make([]int16, maxOpusFrame),
		sum:    make([]int16, frame),
		out:    make([]byte, 1500),
	}, nil
}

// push decodes one of from's packets into its buffer. A packet that won't
// decode is dropped: the source is just silent for that frame.
func (m *portMixer) push(from room.ParticipantID, payload []byte) {
	src := m.srcs[from]
	if src == nil {
		dec, err := m.newDec()
		if err != nil {
			return
		}
		src = &mixSource{dec: dec}
		m.srcs[from] = src
	}
	n, err := src.dec.Decode(payload, m.pcm)
	if err != nil || n <= 0 {
		return
	}
	src.buf = append(src.buf, m.pcm[:n]...)
	if over := len(src.buf) - mixMaxFrames*m.tick; over > 0 {
		src.buf = append(src.buf[:0], src.buf[over:]...)
	}
	if len(src.buf) >= mixPrimeFrames*m.tick {
		src.primed = true
	}
}

// frame mixes the next 20ms of every primed source and encodes it. ok is false
// when no source had audio, so nothing is sent for this tick.
func (m *portMixer) frame() (payload []byte, ok bool) {
	acc := make([]int32, m.tick)
	for _, src := range m.srcs {
		if !src.primed {
			continue
		}
		if len(src.buf) < m.tick {
			src.primed = false // ran dry: re-buffer before mixing it again
			continue
		}
		for i, v := range src.buf[:m.tick] {
			acc[i] += int32(v)
		}
		src.buf = append(src.buf[:0], src.buf[m.tick:]...)
		ok = true
	}
	if !ok {
		return nil, false
	}
	for i, v := range acc {
		m.sum[i] = int16(max(-32768, min(32767, v)))
	}
	n, err := m.enc.Encode(m.sum, m.out)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), m.out[:n]...), true
}

// g711Coder adapts package g711 to the mixer's coder interfaces.
type g711Coder struct{ c room.Codec }

func (g g711Coder) Decode(data []byte, pcm []int16) (int, error) {
	return g711.Decode(g.c, data, pcm), nil
}

func (g g711Coder) Encode(pcm []int16, data []byte) (int, error) {
	return g711.Encode(g.c, pcm, data), nil
}

// drop forgets a publisher (it left, or the port no longer hears it).
func (m *portMixer) drop(pid room.ParticipantID) {
	delete(m.srcs, pid)
}
