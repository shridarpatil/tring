package record

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/room"
)

// More audio than the write buffer holds: every sample must reach the file in
// order, and the header sizes must cover all of it once Close has flushed.
func TestWAVWriterBufferedPastBufferSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leg.wav")
	w, err := newWAVWriter(path, room.PCMA)
	if err != nil {
		t.Fatal(err)
	}
	const frames = 200 // 32000 bytes, ~4x wavBufSize
	for i := 0; i < frames; i++ {
		payload := bytes.Repeat([]byte{byte(i)}, 160)
		if err := w.WriteRTP(&rtp.Packet{Header: rtp.Header{Timestamp: uint32(i * 160)}, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := b[wavHeaderSize:]
	if len(data) != frames*160 {
		t.Fatalf("data = %d bytes, want %d", len(data), frames*160)
	}
	for i := 0; i < frames; i++ {
		if data[i*160] != byte(i) || data[i*160+159] != byte(i) {
			t.Fatalf("frame %d out of order or missing", i)
		}
	}
	if n := binary.LittleEndian.Uint32(b[wavHeaderSize-4:]); int(n) != len(data) {
		t.Fatalf("data size header = %d, want %d", n, len(data))
	}
	if riff := binary.LittleEndian.Uint32(b[4:]); int(riff) != len(b)-8 {
		t.Fatalf("RIFF size = %d, want %d", riff, len(b)-8)
	}
}
