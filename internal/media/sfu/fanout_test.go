package sfu

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
)

// fakeBinding is one subscriber's sender as the fan-out track sees it: Bind
// only needs the codec, SSRC, id and write stream (the embedded nil interface
// covers the rest, which the copy loop never calls).
type fakeBinding struct {
	webrtc.TrackLocalContext
	id   string
	fail bool
	got  atomic.Int64
}

func (b *fakeBinding) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}}
}
func (b *fakeBinding) SSRC() webrtc.SSRC                       { return 1 }
func (b *fakeBinding) SSRCRetransmission() webrtc.SSRC         { return 0 }
func (b *fakeBinding) SSRCForwardErrorCorrection() webrtc.SSRC { return 0 }
func (b *fakeBinding) ID() string                              { return b.id }
func (b *fakeBinding) WriteStream() webrtc.TrackLocalWriter    { return b }

func (b *fakeBinding) WriteRTP(*rtp.Header, []byte) (int, error) {
	if b.fail {
		return 0, io.ErrClosedPipe // a subscriber whose PC/transport is gone
	}
	b.got.Add(1)
	return 0, nil
}
func (b *fakeBinding) Write([]byte) (int, error) { return 0, errors.New("unused") }

// gatedSource emits total packets once gate closes, then EOF.
type gatedSource struct {
	gate  chan struct{}
	sent  int
	total int
}

func (g *gatedSource) ID() room.ParticipantID { return "caller" }
func (g *gatedSource) Close() error           { return nil }
func (g *gatedSource) ReadRTP() (*rtp.Packet, error) {
	<-g.gate
	if g.sent >= g.total {
		return nil, io.EOF
	}
	g.sent++
	return &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(g.sent), PayloadType: 111}, Payload: []byte{0xfc}}, nil
}

// One subscriber whose write fails (its PC closed while its sender is still
// bound, e.g. a listener's tab dropped during the reconnect grace) must not
// stop the publication's copy loop: every other listener keeps hearing it.
func TestFanoutKeepsForwardingWhenOneSubscriberFails(t *testing.T) {
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	const rid = room.RoomID("call")
	const total = 10
	src := &gatedSource{gate: make(chan struct{}), total: total}
	if err := s.InjectSource(rid, src, webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	track := s.rooms[rid].pubs["caller"].tracks[webrtc.RTPCodecTypeAudio]
	s.mu.Unlock()

	broken, healthy := &fakeBinding{id: "broken", fail: true}, &fakeBinding{id: "healthy"}
	for _, b := range []*fakeBinding{broken, healthy} {
		if _, err := track.Bind(b); err != nil {
			t.Fatal(err)
		}
	}
	close(src.gate)

	deadline := time.Now().Add(2 * time.Second)
	for healthy.got.Load() < total && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := healthy.got.Load(); got != total {
		t.Fatalf("healthy subscriber got %d/%d packets; one subscriber's write error stopped the fan-out", got, total)
	}
}
