package prompt_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
)

// In a phone room the player sends the law's static payload type on the 8 kHz
// RTP clock.
func TestPlayerG711PayloadTypeAndClock(t *testing.T) {
	for c, pt := range map[room.Codec]uint8{room.PCMA: 8, room.PCMU: 0} {
		p := prompt.NewPlayer("ivr", c, time.Millisecond)
		p.Play([][]byte{make([]byte, 160), make([]byte, 160)})
		a, _ := p.ReadRTP()
		b, _ := p.ReadRTP()
		_ = p.Close()
		if a.PayloadType != pt || b.PayloadType != pt {
			t.Fatalf("%s: payload types %d,%d, want %d", c, a.PayloadType, b.PayloadType, pt)
		}
		if b.Timestamp-a.Timestamp != 160 {
			t.Fatalf("%s: timestamp step %d, want 160", c, b.Timestamp-a.Timestamp)
		}
	}
}

func TestSplitG711DropsShortTail(t *testing.T) {
	frames := prompt.SplitG711(make([]byte, 2*160+70))
	if len(frames) != 2 || len(frames[0]) != 160 || len(frames[1]) != 160 {
		t.Fatalf("got %d frames, want 2 of 160 bytes", len(frames))
	}
}

// A stored Ogg/Opus prompt decodes to 20ms G.711 frames: 1s → 50 frames.
func TestLoadG711FromOggOpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "prompt.ogg")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-ac", "1", "-ar", "48000", "-c:a", "libopus", path).CombinedOutput(); err != nil {
		t.Fatalf("gen prompt: %v: %s", err, b)
	}
	for _, c := range []room.Codec{room.PCMA, room.PCMU} {
		frames, err := prompt.LoadG711(context.Background(), path, c)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if n := len(frames); n < 49 || n > 50 {
			t.Fatalf("%s: %d frames, want ~50 for 1s", c, n)
		}
	}
	if _, err := prompt.LoadG711(context.Background(), filepath.Join(t.TempDir(), "missing.ogg"), room.PCMA); err == nil {
		t.Fatal("missing file decoded without error")
	}
}
