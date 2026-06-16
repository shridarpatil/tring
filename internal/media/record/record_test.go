package record_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"github.com/whatomate/tring/internal/media/record"
	"github.com/whatomate/tring/internal/media/room"
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
