package record_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/record"
	"github.com/shridarpatil/tring/internal/media/room"
)

// opusPkt builds a minimal valid Opus RTP packet (any non-empty payload is a
// valid Opus packet to pion's depacketizer).
func opusPkt(ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, Timestamp: ts},
		Payload: []byte{0x78, 0x01, 0x02, 0x03}, // TOC + a few bytes
	}
}

func TestRecorderWritesPerParticipantOgg(t *testing.T) {
	dir := t.TempDir()
	rec := record.New(dir, "call-1")
	rec.Label("p-agent", "agent")
	rec.Label("p-cust", "customer")
	rec.Arm() // capture is gated until answer; arm so the writes land

	// Feed 50 packets each (1s @ 20ms) for two participants.
	for i := range 50 {
		ts := uint32(i * 960) // 20ms @ 48kHz
		if err := rec.WriteRTP("p-agent", opusPkt(ts)); err != nil {
			t.Fatalf("write agent: %v", err)
		}
		if err := rec.WriteRTP("p-cust", opusPkt(ts)); err != nil {
			t.Fatalf("write customer: %v", err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Two role-named Ogg files, each a real Ogg stream (starts with "OggS").
	for _, role := range []string{"agent", "customer"} {
		path := filepath.Join(dir, "call-1-"+role+".ogg")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", role, err)
		}
		if len(b) < 64 {
			t.Errorf("%s.ogg too small: %d bytes", role, len(b))
		}
		if string(b[:4]) != "OggS" {
			t.Errorf("%s.ogg missing OggS magic: %x", role, b[:4])
		}
	}

	tracks := rec.Tracks()
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
}

// TestRecorderDropsUntilArmed guards "recording starts at agent answer": packets
// written before Arm (the pre-answer IVR/ringing/queue phase) are dropped — no
// file, no track — and only packets after Arm are captured.
func TestRecorderDropsUntilArmed(t *testing.T) {
	dir := t.TempDir()
	rec := record.New(dir, "c")
	rec.Label("cust", "customer")

	for i := range 10 { // pre-answer: must be dropped
		if err := rec.WriteRTP("cust", opusPkt(uint32(i*960))); err != nil {
			t.Fatalf("pre-arm write: %v", err)
		}
	}
	if n := len(rec.Tracks()); n != 0 {
		t.Fatalf("tracks before Arm = %d, want 0 (pre-answer audio must not be recorded)", n)
	}

	rec.Arm()
	for i := range 10 { // post-answer: captured
		if err := rec.WriteRTP("cust", opusPkt(uint32((10+i)*960))); err != nil {
			t.Fatalf("post-arm write: %v", err)
		}
	}
	_ = rec.Close()

	if n := len(rec.Tracks()); n != 1 {
		t.Fatalf("tracks after Arm = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "c-customer.ogg")); err != nil {
		t.Fatalf("expected c-customer.ogg after Arm: %v", err)
	}
}

func TestWriteAfterCloseIsNoop(t *testing.T) {
	rec := record.New(t.TempDir(), "c")
	if err := rec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := rec.WriteRTP("p", opusPkt(0)); err != nil {
		t.Fatalf("write after close should be a no-op, got %v", err)
	}
	if err := rec.Close(); err != nil { // idempotent
		t.Fatalf("second close: %v", err)
	}
}

func TestUnlabelledParticipantUsesID(t *testing.T) {
	dir := t.TempDir()
	rec := record.New(dir, "c")
	rec.Arm()
	if err := rec.WriteRTP(room.ParticipantID("xyz"), opusPkt(0)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = rec.Close()
	if _, err := os.Stat(filepath.Join(dir, "c-xyz.ogg")); err != nil {
		t.Fatalf("expected c-xyz.ogg: %v", err)
	}
}

// TestMergeStereoMissingFFmpeg only asserts the graceful sentinel when ffmpeg is
// absent; the real merge is exercised in environments that ship ffmpeg.
func TestMergeStereoGraceful(t *testing.T) {
	dir := t.TempDir()
	rec := record.New(dir, "c")
	rec.Label("a", "agent")
	rec.Label("b", "customer")
	rec.Arm()
	for i := range 10 {
		_ = rec.WriteRTP("a", opusPkt(uint32(i*960)))
		_ = rec.WriteRTP("b", opusPkt(uint32(i*960)))
	}
	_ = rec.Close()

	left := filepath.Join(dir, "c-agent.ogg")
	right := filepath.Join(dir, "c-customer.ogg")
	out := filepath.Join(dir, "c-stereo.ogg")
	err := record.MergeStereo(context.Background(), out, left, right)
	if err == record.ErrFFmpegMissing {
		t.Skip("ffmpeg not installed — merge skipped")
	}
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if fi, statErr := os.Stat(out); statErr != nil || fi.Size() == 0 {
		t.Fatalf("stereo output missing/empty: %v", statErr)
	}
}

// TestMergeStereoSeparatesChannels guards the "agent LEFT, customer RIGHT" rule:
// the two legs must land on DIFFERENT channels. Each per-leg file record.go
// writes carries a 2-channel Opus header (opusChannels=2) though it holds one
// person's mono mic — and ffmpeg's `join` with no explicit map fills the stereo
// output entirely from input 0 (agent), silently dropping input 1 (customer).
// That produced two identical channels (agent on both), exactly the "I can hear
// the agent but not the visitor" report. We feed two distinct tones and assert
// the channels come out distinct.
func TestMergeStereoSeparatesChannels(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.ogg")    // → FL
	customer := filepath.Join(dir, "customer.ogg") // → FR
	genTone(t, agent, 300)
	genTone(t, customer, 1200)

	out := filepath.Join(dir, "stereo.ogg")
	if err := record.MergeStereo(context.Background(), out, agent, customer); err != nil {
		t.Fatalf("merge: %v", err)
	}

	left, right := decodeStereo(t, out)
	rmsL, rmsR := rms(left), rms(right)
	if rmsL < 1000 || rmsR < 1000 {
		t.Fatalf("a channel is unexpectedly silent: L=%.0f R=%.0f (a leg was dropped)", rmsL, rmsR)
	}
	// The bug duplicated the agent leg into both channels → identical L/R. Distinct
	// tones must yield distinct channels.
	if d := meanAbsDiff(left, right); d < 1000 {
		t.Fatalf("left/right channels are ~identical (meanAbsDiff=%.0f): customer leg was dropped, not placed on the right", d)
	}
}

// genTone writes a 1s sine tone of freq Hz as a 2-channel Ogg/Opus file — the
// same 2-channel header record.go emits per leg, so the merge sees the exact
// shape that triggered the join bug.
func genTone(t *testing.T, path string, freq int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=%d:duration=1:sample_rate=48000", freq),
		"-ac", "2", "-c:a", "libopus", path)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gen tone %dHz: %v: %s", freq, err, b)
	}
}

// decodeStereo decodes an Ogg/Opus file to interleaved 48k s16le and splits it
// into left/right sample slices.
func decodeStereo(t *testing.T, path string) (left, right []int16) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-i", path, "-f", "s16le", "-acodec", "pcm_s16le",
		"-ac", "2", "-ar", "48000", "-")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	raw := buf.Bytes()
	for i := 0; i+3 < len(raw); i += 4 {
		left = append(left, int16(binary.LittleEndian.Uint16(raw[i:])))
		right = append(right, int16(binary.LittleEndian.Uint16(raw[i+2:])))
	}
	return left, right
}

func rms(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

func meanAbsDiff(a, b []int16) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var sum float64
	for i := range n {
		d := float64(a[i]) - float64(b[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / float64(n)
}
