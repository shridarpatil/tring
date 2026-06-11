package sfu_test

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/media/sfu"
)

// fakeSource is a room.ParticipantSource that emits `total` Opus packets then
// EOF — an externally-fed participant (the IVR prompt player / FS phone leg
// stand-in) with no PeerConnection.
type fakeSource struct {
	id    room.ParticipantID
	mu    sync.Mutex
	sent  int
	total int
}

func (f *fakeSource) ID() room.ParticipantID { return f.id }
func (f *fakeSource) Close() error           { return nil }
func (f *fakeSource) ReadRTP() (*rtp.Packet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent >= f.total {
		return nil, io.EOF
	}
	f.sent++
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: uint16(f.sent), PayloadType: 111},
		Payload: []byte{0xfc, 0xff, 0xfe},
	}, nil
}

// countingSink is a room.TrackSink that records what it received.
type countingSink struct {
	n   atomic.Int64
	pid atomic.Value // room.ParticipantID
}

func (c *countingSink) WriteRTP(pid room.ParticipantID, _ *rtp.Packet) error {
	c.pid.Store(pid)
	c.n.Add(1)
	return nil
}
func (c *countingSink) Close() error { return nil }

// Compile-time proof the fakes satisfy the extension interfaces.
var (
	_ room.ParticipantSource = (*fakeSource)(nil)
	_ room.TrackSink         = (*countingSink)(nil)
)

// TestInjectSourceTeesToSink exercises BOTH extension seams without ICE: an
// externally-fed ParticipantSource is injected as a room publication, and a
// TrackSink attached to it receives every packet. This is the foundation for
// the M8 IVR participant (publishes prompts as a source, hears the caller via a
// sink) and the Phase-2 phone leg.
func TestInjectSourceTeesToSink(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	const total = 30
	src := &fakeSource{id: "ivr", total: total}
	sink := &countingSink{}

	// Attach the sink before the source publishes (sink may pre-exist the media).
	s.AddSink("call", "ivr", sink)
	if err := s.InjectSource("call", src, webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}); err != nil {
		t.Fatalf("inject: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.n.Load() >= total {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := sink.n.Load(); got != total {
		t.Fatalf("sink received %d packets, want %d", got, total)
	}
	if pid, _ := sink.pid.Load().(room.ParticipantID); pid != "ivr" {
		t.Fatalf("sink saw pid %q, want ivr", pid)
	}
}
