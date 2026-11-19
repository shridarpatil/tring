package sfu

import (
	"bytes"
	"testing"

	"github.com/shridarpatil/tring/internal/media/room"
)

// Known G.711 code points: silence encodes to the law's zero byte, and each
// code decodes to a value that re-encodes to the same code (the quantizer is
// idempotent on its own outputs).
func TestG711CodePoints(t *testing.T) {
	if b := linearToAlaw(0); b != 0xD5 {
		t.Fatalf("A-law silence = %#x, want 0xD5", b)
	}
	if b := linearToUlaw(0); b != 0xFF {
		t.Fatalf("µ-law silence = %#x, want 0xFF", b)
	}
	for i := range 256 {
		if got := linearToAlaw(alawToLinear(byte(i))); got != byte(i) {
			t.Fatalf("A-law %#x round-trips to %#x", i, got)
		}
		u := byte(i)
		if u == 0x7F { // µ-law has two zeros (0xFF and 0x7F); both decode to 0
			continue
		}
		if got := linearToUlaw(ulawToLinear(u)); got != u {
			t.Fatalf("µ-law %#x round-trips to %#x", i, got)
		}
	}
	if v := alawToLinear(linearToAlaw(32767)); v < 32000 {
		t.Fatalf("A-law full scale decodes to %d", v)
	}
	if v := ulawToLinear(linearToUlaw(-32768)); v > -32000 {
		t.Fatalf("µ-law negative full scale decodes to %d", v)
	}
}

// A barge in a PCMA room mixes without libopus: two speakers' samples are
// summed and the frame goes out as 160 A-law bytes (20ms at 8kHz).
func TestMixerMixesG711WithoutLibopus(t *testing.T) {
	dec, enc := newOpusDecoder, newOpusEncoder
	newOpusDecoder, newOpusEncoder = nil, nil // a cgo-free build
	t.Cleanup(func() { newOpusDecoder, newOpusEncoder = dec, enc })

	if m, _ := newPortMixer(room.Opus); m != nil {
		t.Fatal("Opus mixer built without libopus")
	}
	m, err := newPortMixer(room.PCMA)
	if err != nil || m == nil {
		t.Fatalf("newPortMixer(PCMA) = %v, %v", m, err)
	}
	a := bytes.Repeat([]byte{linearToAlaw(1000)}, 160)
	b := bytes.Repeat([]byte{linearToAlaw(2000)}, 160)
	for range mixPrimeFrames {
		m.push("agent", a)
		m.push("supervisor", b)
	}
	out, ok := m.frame()
	if !ok || len(out) != 160 {
		t.Fatalf("frame = %d bytes, ok=%v; want 160", len(out), ok)
	}
	sum := int(alawToLinear(a[0])) + int(alawToLinear(b[0]))
	got := int(alawToLinear(out[0]))
	if d := got - sum; d < -128 || d > 128 { // within one A-law step at this level
		t.Fatalf("mixed sample = %d, want ≈ %d", got, sum)
	}
}
