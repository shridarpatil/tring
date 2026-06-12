package prompt_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/whatomate/tring/internal/media/prompt"
	"github.com/whatomate/tring/internal/media/room"
)

// compile-time: the player is a ParticipantSource (injectable into a room).
var _ room.ParticipantSource = (*prompt.Player)(nil)

func TestPlayerPacketizesFramesWithIncrementingSeqTs(t *testing.T) {
	p := prompt.NewPlayer("ivr", time.Millisecond) // tiny pacing for a fast test
	frames := [][]byte{{1}, {2, 2}, {3, 3, 3}}
	done := p.Play(frames)

	for i, want := range frames {
		pkt, err := p.ReadRTP()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(pkt.Payload) != string(want) {
			t.Fatalf("packet %d payload = %v, want %v", i, pkt.Payload, want)
		}
		if int(pkt.SequenceNumber) != i {
			t.Fatalf("packet %d seq = %d, want %d", i, pkt.SequenceNumber, i)
		}
		if pkt.Timestamp != uint32(i*960) {
			t.Fatalf("packet %d ts = %d, want %d", i, pkt.Timestamp, i*960)
		}
		if pkt.PayloadType != 111 {
			t.Fatalf("packet %d pt = %d, want 111 (opus)", i, pkt.PayloadType)
		}
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("playback done channel never closed after draining frames")
	}
}

func TestPlayerCloseUnblocksRead(t *testing.T) {
	p := prompt.NewPlayer("ivr", time.Millisecond)
	errc := make(chan error, 1)
	go func() {
		_, err := p.ReadRTP() // blocks (no frames) until Close
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = p.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected error after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("ReadRTP did not unblock on Close")
	}
}

func TestPlayEmptyBatchClosesImmediately(t *testing.T) {
	p := prompt.NewPlayer("ivr", time.Millisecond)
	select {
	case <-p.Play(nil):
	case <-time.After(time.Second):
		t.Fatal("empty Play should return an already-closed channel")
	}
}

// TestLoadOggOpusSplitsMultiPacketPages guards the lacing fix: a real
// ffmpeg-encoded Opus prompt packs many packets per Ogg page, and LoadOggOpus
// must split them into one frame each (else a multi-second prompt collapses to a
// handful of frames that play in a blink). Skips when ffmpeg isn't installed.
func TestLoadOggOpusSplitsMultiPacketPages(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "tone.ogg")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-ac", "1", "-ar", "48000", "-c:a", "libopus", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, b)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	frames, err := prompt.LoadOggOpus(f)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 2s @ 20ms ≈ 100 frames. The old page-as-frame bug returned only a handful.
	if len(frames) < 80 {
		t.Fatalf("got %d frames for a 2s prompt — multi-packet pages not split", len(frames))
	}
}
