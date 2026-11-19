package record

import (
	"encoding/binary"
	"os"

	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/room"
)

// wavWriter captures one G.711 leg (a phone room) as an 8kHz mono A-law or
// µ-law WAV file. Like the Ogg/Opus capture it stores the RTP payload as is —
// no transcode — and ffmpeg reads it natively at merge. Unlike Ogg there are no
// per-packet timestamps, so a gap in the RTP timeline (packet loss) is filled
// with silence; otherwise the leg would drift ahead of the other channel.
type wavWriter struct {
	f       *os.File
	silence byte   // the law's zero-amplitude sample
	n       uint32 // data bytes written
	started bool
	nextTS  uint32 // RTP timestamp the next packet should carry
}

// wavHeaderSize is the RIFF header, an 18-byte fmt chunk (non-PCM formats
// carry cbSize) and the data chunk header.
const wavHeaderSize = 12 + 8 + 18 + 8

// maxGapFill bounds the silence filled for one gap: 5s. A bigger jump is a
// new stream (a browser reconnect starts a fresh timestamp base), not loss.
const maxGapFill = 5 * 8000

func newWAVWriter(path string, c room.Codec) (*wavWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	format, silence := uint16(6), byte(0xD5) // WAVE_FORMAT_ALAW
	if c == room.PCMU {
		format, silence = 7, 0xFF // WAVE_FORMAT_MULAW
	}
	h := make([]byte, wavHeaderSize)
	copy(h[0:], "RIFF")
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 18)
	binary.LittleEndian.PutUint16(h[20:], format)
	binary.LittleEndian.PutUint16(h[22:], 1)    // mono
	binary.LittleEndian.PutUint32(h[24:], 8000) // sample rate
	binary.LittleEndian.PutUint32(h[28:], 8000) // byte rate
	binary.LittleEndian.PutUint16(h[32:], 1)    // block align
	binary.LittleEndian.PutUint16(h[34:], 8)    // bits per sample
	// h[36:38] cbSize = 0
	copy(h[38:], "data")
	if _, err := f.Write(h); err != nil { // sizes are patched in on Close
		_ = f.Close()
		return nil, err
	}
	return &wavWriter{f: f, silence: silence}, nil
}

// WriteRTP appends pkt's samples, first filling any timestamp gap since the
// previous packet with silence. A late (reordered) packet is dropped.
func (w *wavWriter) WriteRTP(pkt *rtp.Packet) error {
	if w.started {
		gap := int32(pkt.Timestamp - w.nextTS)
		switch {
		case gap < 0 && gap > -maxGapFill:
			return nil // late or duplicate: its slot is already written
		case gap > 0 && gap <= maxGapFill:
			if err := w.write(silenceRun(w.silence, int(gap))); err != nil {
				return err
			}
		}
	}
	w.started = true
	w.nextTS = pkt.Timestamp + uint32(len(pkt.Payload))
	return w.write(pkt.Payload)
}

func (w *wavWriter) write(b []byte) error {
	n, err := w.f.Write(b)
	w.n += uint32(n)
	return err
}

// Close patches the RIFF and data sizes into the header and closes the file.
func (w *wavWriter) Close() error {
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], wavHeaderSize-8+w.n)
	_, err1 := w.f.WriteAt(sz[:], 4)
	binary.LittleEndian.PutUint32(sz[:], w.n)
	_, err2 := w.f.WriteAt(sz[:], wavHeaderSize-4)
	err3 := w.f.Close()
	for _, err := range []error{err1, err2, err3} {
		if err != nil {
			return err
		}
	}
	return nil
}

func silenceRun(b byte, n int) []byte {
	s := make([]byte, n)
	for i := range s {
		s[i] = b
	}
	return s
}
