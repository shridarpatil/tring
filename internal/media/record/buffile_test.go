package record

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

// opusPackets is n fake 20 ms Opus packets; the writers don't decode payloads.
func opusPackets(n int) []*rtp.Packet {
	pkts := make([]*rtp.Packet, n)
	for i := range pkts {
		pkts[i] = &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i * 960)},
			Payload: bytes.Repeat([]byte{byte(i), 0x5a}, 30+i%20),
		}
	}
	return pkts
}

// oggPage is the parts of an Ogg page that must match between writers
// (oggwriter picks a random serial per stream, which also changes each CRC).
type oggPage struct {
	headerType byte
	granule    uint64
	seq        uint32
	body       string // segment table + payload
}

// readOggPages splits an Ogg file into pages, failing on a bad CRC.
func readOggPages(t *testing.T, b []byte) []oggPage {
	t.Helper()
	var pages []oggPage
	for len(b) > 0 {
		if len(b) < oggPageHeaderSize || string(b[:4]) != "OggS" {
			t.Fatalf("bad page at %d bytes from the end", len(b))
		}
		nseg := int(b[26])
		size := oggPageHeaderSize + nseg
		for _, l := range b[oggPageHeaderSize : oggPageHeaderSize+nseg] {
			size += int(l)
		}
		page := append([]byte(nil), b[:size]...)
		crc := binary.LittleEndian.Uint32(page[oggCRCOffset:])
		binary.LittleEndian.PutUint32(page[oggCRCOffset:], 0)
		if oggCRC(page) != crc {
			t.Fatalf("page %d: bad CRC", len(pages))
		}
		pages = append(pages, oggPage{
			headerType: page[5],
			granule:    binary.LittleEndian.Uint64(page[6:]),
			seq:        binary.LittleEndian.Uint32(page[18:]),
			body:       string(page[oggPageHeaderSize:]),
		})
		b = b[size:]
	}
	return pages
}

// The buffered Ogg file must match oggwriter.New's own file page for page,
// end-of-stream flag on the last page included, with valid CRCs.
func TestOggFileMatchesUnbufferedOggwriter(t *testing.T) {
	// Not 0: the Recorder opens a leg's file on its first packet, so a file
	// always holds audio (with none, oggwriter.New stamps its EOS page granule
	// 1 rather than the last page's 0). 500 packets ≈ 3x oggBufSize.
	for _, n := range []int{1, 500} {
		dir := t.TempDir()
		want := filepath.Join(dir, "want.ogg")
		got := filepath.Join(dir, "got.ogg")

		ref, err := oggwriter.New(want, opusRate, opusChannels)
		if err != nil {
			t.Fatal(err)
		}
		of, err := newOggFile(got)
		if err != nil {
			t.Fatal(err)
		}
		bw, err := oggwriter.NewWith(of, opusRate, opusChannels)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range opusPackets(n) {
			if err := ref.WriteRTP(p); err != nil {
				t.Fatal(err)
			}
			if err := bw.WriteRTP(p); err != nil {
				t.Fatal(err)
			}
		}
		if err := ref.Close(); err != nil {
			t.Fatal(err)
		}
		if err := bw.Close(); err != nil {
			t.Fatal(err)
		}
		wb, _ := os.ReadFile(want)
		gb, _ := os.ReadFile(got)
		wp, gp := readOggPages(t, wb), readOggPages(t, gb)
		if len(wp) != len(gp) {
			t.Fatalf("%d packets: %d pages, want %d", n, len(gp), len(wp))
		}
		for i := range wp {
			if wp[i] != gp[i] {
				t.Fatalf("%d packets: page %d type/granule/seq = %#x/%d/%d, want %#x/%d/%d (body equal %v)", n, i, gp[i].headerType, gp[i].granule, gp[i].seq, wp[i].headerType, wp[i].granule, wp[i].seq, gp[i].body == wp[i].body)
			}
		}
		if last := gp[len(gp)-1].headerType; last != oggHeaderTypeEOS {
			t.Fatalf("%d packets: last page type %#x, want end of stream", n, last)
		}
	}
}

// vp8Keyframes is n single-packet VP8 keyframes (payload descriptor S bit,
// then a frame whose first byte's low bit is 0 = keyframe).
func vp8Keyframes(n int) []*rtp.Packet {
	pkts := make([]*rtp.Packet, n)
	for i := range pkts {
		frame := append([]byte{0x10, 0x00, 0x00, 0x00, 0x9d, 0x01, 0x2a}, bytes.Repeat([]byte{byte(i)}, 1000)...)
		pkts[i] = &rtp.Packet{
			Header:  rtp.Header{Marker: true, SequenceNumber: uint16(i), Timestamp: uint32(i * 3000)},
			Payload: frame,
		}
	}
	return pkts
}

// The buffered IVF file, frame count patched in on Close, must be
// byte-identical to ivfwriter.New's own file.
func TestBufferedFileIVFMatchesUnbufferedIvfwriter(t *testing.T) {
	opts := func() []ivfwriter.Option {
		return []ivfwriter.Option{ivfwriter.WithCodec("video/VP8"), ivfwriter.WithFrameRate(1, 90000), ivfwriter.WithDirectPTS()}
	}
	dir := t.TempDir()
	want := filepath.Join(dir, "want.ivf")
	got := filepath.Join(dir, "got.ivf")

	ref, err := ivfwriter.New(want, opts()...)
	if err != nil {
		t.Fatal(err)
	}
	bf, err := newBufferedFile(got, videoBufSize)
	if err != nil {
		t.Fatal(err)
	}
	bw, err := ivfwriter.NewWith(bf, opts()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range vp8Keyframes(100) { // ~100 KB ≈ 3x videoBufSize
		if err := ref.WriteRTP(p); err != nil {
			t.Fatal(err)
		}
		if err := bw.WriteRTP(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := ref.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	wb, _ := os.ReadFile(want)
	gb, _ := os.ReadFile(got)
	if len(wb) < 32 {
		t.Fatalf("reference IVF too small (%d bytes): test packets not accepted", len(wb))
	}
	if !bytes.Equal(wb, gb) {
		t.Fatalf("buffered IVF differs from ivfwriter.New (%d vs %d bytes)", len(gb), len(wb))
	}
}
