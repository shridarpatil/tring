package record

import (
	"fmt"
	"testing"

	"github.com/pion/rtp"
)

func capsVP8(ts uint32, key bool) *rtp.Packet {
	frame := byte(1)
	if key {
		frame = 0
	}
	return &rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: ts, Marker: true}, Payload: []byte{0x10, frame, 0, 0, 0}}
}

// TestVideoSegmentCap: toggling video on and off can't add segments without
// bound (each one is a decode/overlay chain in the composite ffmpeg run).
func TestVideoSegmentCap(t *testing.T) {
	r := New(t.TempDir(), "call")
	r.capVideoSegments = 3
	r.Arm()
	for i := 0; i < 10; i++ {
		stream := fmt.Sprintf("s%d", i)
		if err := r.WriteVideoRTP("v", stream, "video/VP8", capsVP8(0, true)); err != nil {
			t.Fatal(err)
		}
		r.EndVideo("v", stream)
	}
	if got := len(r.VideoTracks()); got != 3 {
		t.Fatalf("video segments = %d, want cap 3", got)
	}
}

// TestVideoByteCap: past the byte cap the open capture is closed and kept, and
// later video (even a new stream) isn't recorded.
func TestVideoByteCap(t *testing.T) {
	r := New(t.TempDir(), "call")
	r.capVideoBytes = 12 // two 5-byte packets fit, the third doesn't
	r.Arm()
	for ts := uint32(0); ts < 5; ts++ {
		if err := r.WriteVideoRTP("v", "cam", "video/VP8", capsVP8(ts*3000, ts == 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.WriteVideoRTP("v", "screen", "video/VP8", capsVP8(0, true)); err != nil {
		t.Fatal(err)
	}
	tracks := r.VideoTracks()
	if len(tracks) != 1 {
		t.Fatalf("video tracks = %d, want only the capped one", len(tracks))
	}
	if r.videos["cam"].frames != 2 || r.videos["cam"].end.IsZero() {
		t.Fatalf("capped capture: frames=%d ended=%v, want 2 frames and closed", r.videos["cam"].frames, !r.videos["cam"].end.IsZero())
	}
}

// TestAudioByteCap: one leg past its cap stops growing; other legs don't.
func TestAudioByteCap(t *testing.T) {
	r := New(t.TempDir(), "call")
	r.capAudioBytes = 10
	r.Arm()
	pkt := func(seq uint16) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{1, 2, 3, 4}}
	}
	for i := uint16(0); i < 6; i++ {
		_ = r.WriteRTP("loud", pkt(i)) // 24 bytes offered
	}
	for i := uint16(0); i < 2; i++ {
		_ = r.WriteRTP("quiet", pkt(i))
	}
	if r.audioBytes["loud"] != 8 {
		t.Fatalf("loud leg bytes = %d, want 8 (capped at 10)", r.audioBytes["loud"])
	}
	if r.audioBytes["quiet"] != 8 {
		t.Fatalf("quiet leg bytes = %d, want 8", r.audioBytes["quiet"])
	}
}
