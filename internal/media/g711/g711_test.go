package g711

import (
	"testing"

	"github.com/shridarpatil/tring/internal/media/room"
)

// Known G.711 code points: silence encodes to the law's zero byte, and each
// code decodes to a value that re-encodes to the same code (the quantizer is
// idempotent on its own outputs).
func TestCodePoints(t *testing.T) {
	if b := linearToAlaw(0); b != Silence(room.PCMA) {
		t.Fatalf("A-law silence = %#x, want %#x", b, Silence(room.PCMA))
	}
	if b := linearToUlaw(0); b != Silence(room.PCMU) {
		t.Fatalf("µ-law silence = %#x, want %#x", b, Silence(room.PCMU))
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

// Encode/Decode code whole buffers, bounded by the shorter side.
func TestEncodeDecodeBuffers(t *testing.T) {
	for _, c := range []room.Codec{room.PCMA, room.PCMU} {
		pcm := []int16{0, 1000, -1000, 20000}
		data := make([]byte, 3) // shorter than pcm
		if n := Encode(c, pcm, data); n != 3 {
			t.Fatalf("%s: Encode wrote %d, want 3", c, n)
		}
		back := make([]int16, 8)
		if n := Decode(c, data, back); n != 3 {
			t.Fatalf("%s: Decode wrote %d, want 3", c, n)
		}
		for i, want := range pcm[:3] {
			if d := int(back[i]) - int(want); d < -64 || d > 64 {
				t.Fatalf("%s: sample %d = %d, want ≈ %d", c, i, back[i], want)
			}
		}
	}
}
