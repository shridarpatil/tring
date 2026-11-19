package record_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/record"
	"github.com/shridarpatil/tring/internal/media/room"
)

func pcmaPkt(ts uint32, payload []byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, Timestamp: ts}, Payload: payload}
}

// A phone room's G.711 leg is captured as an A-law WAV, byte for byte, with a
// lost packet's slot filled with silence (so the leg keeps its place on the
// stereo timeline) and a late duplicate dropped.
func TestRecorderWritesG711LegAsWAV(t *testing.T) {
	dir := t.TempDir()
	rec := record.New(dir, "call1")
	rec.Arm()
	frame := bytes.Repeat([]byte{0x2A}, 160)
	for _, ts := range []uint32{1000, 1160, 1480, 1160} { // 1320 lost; 1160 again is late
		if err := rec.WriteRTP("caller", pcmaPkt(ts, frame)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	tracks := rec.Tracks()
	if len(tracks) != 1 || !strings.HasSuffix(tracks[0].Path, ".wav") {
		t.Fatalf("tracks = %+v, want one .wav leg", tracks)
	}
	b, err := os.ReadFile(tracks[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	const header = 46
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[38:42]) != "data" {
		t.Fatalf("not a WAV header: % x", b[:header])
	}
	if f := binary.LittleEndian.Uint16(b[20:]); f != 6 {
		t.Fatalf("format tag = %d, want 6 (A-law)", f)
	}
	data := b[header:]
	if n := binary.LittleEndian.Uint32(b[42:]); int(n) != len(data) || len(data) != 4*160 {
		t.Fatalf("data size = %d (header) / %d (file), want 640", n, len(data))
	}
	if riff := binary.LittleEndian.Uint32(b[4:]); int(riff) != len(b)-8 {
		t.Fatalf("RIFF size = %d, want %d", riff, len(b)-8)
	}
	if !bytes.Equal(data[320:480], bytes.Repeat([]byte{0xD5}, 160)) {
		t.Fatal("the lost packet's slot is not A-law silence")
	}
	if !bytes.Equal(data[480:], frame) {
		t.Fatal("the packet after the gap is not where it belongs")
	}
}

// The existing merge takes G.711 WAV legs as they are: agent left, customer
// right, distinct and audible.
func TestMergeStereoG711Legs(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	rec := record.New(dir, "call1")
	rec.Arm()
	for pid, freq := range map[string]int{"agent": 300, "customer": 1200} {
		raw := alawTone(t, freq)
		for i := 0; i+160 <= len(raw); i += 160 {
			if err := rec.WriteRTP(room.ParticipantID(pid), pcmaPkt(uint32(i), raw[i:i+160])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, tr := range rec.Tracks() {
		paths[string(tr.PID)] = tr.Path
	}
	out := filepath.Join(dir, "stereo.ogg")
	if err := record.MergeStereo(context.Background(), out, paths["agent"], paths["customer"], 8000); err != nil {
		t.Fatalf("merge: %v", err)
	}
	left, right := decodeStereo(t, out)
	if rms(left) < 1000 || rms(right) < 1000 {
		t.Fatalf("a channel is silent: L=%.0f R=%.0f", rms(left), rms(right))
	}
	if d := meanAbsDiff(left, right); d < 1000 {
		t.Fatalf("left/right ~identical (meanAbsDiff=%.0f)", d)
	}
}

// alawTone is 1s of a freq Hz sine as raw 8kHz A-law.
func alawTone(t *testing.T, freq int) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=%d:duration=1:sample_rate=8000", freq), "-f", "alaw", "-")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gen A-law tone: %v", err)
	}
	return out
}
