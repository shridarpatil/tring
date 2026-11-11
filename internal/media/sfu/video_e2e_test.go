package sfu_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/shridarpatil/tring/internal/media/record"
	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

type audioOnlySink struct{ bad atomic.Bool }

func (s *audioOnlySink) WriteRTP(_ room.ParticipantID, p *rtp.Packet) error {
	// The test sends zero-filled audio samples and a VP8 payload descriptor for video.
	if len(p.Payload) > 0 && p.Payload[0] != 0 {
		s.bad.Store(true)
	}
	return nil
}
func (*audioOnlySink) Close() error { return nil }

// Exercise the browser's dedicated sendonly transceiver flow after receiving
// the other side's video, then stop/restart while both microphones keep flowing.
func TestTwoWayVideoRestartAndRecording(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var closers []func()
	defer func() {
		for _, close := range closers {
			close()
		}
	}()
	a := newClient(t, s, clientAPI(t), "video", "agent", &closers)
	b := newClient(t, s, clientAPI(t), "video", "client", &closers)
	defer s.Leave("video", "agent")
	defer s.Leave("video", "client")
	var aa, av, ba, bv atomic.Int64
	count := func(c *client, audio, video *atomic.Int64) {
		c.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
				if tr.Kind() == webrtc.RTPCodecTypeAudio {
					audio.Add(1)
				} else {
					video.Add(1)
				}
			}
		})
	}
	count(a, &aa, &av)
	count(b, &ba, &bv)
	rec := record.New(t.TempDir(), "video")
	rec.Label("agent", "agent")
	rec.Label("client", "customer")
	rec.Arm()
	defer rec.Close()
	audioSink := &audioOnlySink{}
	for _, pid := range []room.ParticipantID{"agent", "client"} {
		s.AddSink("video", pid, rec)
		s.AddSink("video", pid, audioSink)
	}
	a.publishOpus(t, &closers)
	b.publishOpus(t, &closers)
	a.connect()
	b.connect()
	wait := func(label string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", label)
	}
	wait("initial audio", func() bool { return aa.Load() > 5 && ba.Load() > 5 })
	start := func(c *client) *webrtc.RTPTransceiver {
		track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "camera")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := c.pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		closers = append(closers, func() { close(stop) })
		go func() {
			tick := time.NewTicker(33 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					_ = track.WriteSample(media.Sample{Data: make([]byte, 200), Duration: 33 * time.Millisecond})
				}
			}
		}()
		c.server.Publish(webrtc.RTPCodecTypeVideo)
		return tx
	}
	first := start(a)
	wait("client receives agent camera", func() bool { return bv.Load() > 5 })
	start(b)
	wait("both cameras recorded", func() bool { return av.Load() > 5 && len(rec.VideoTracks()) == 2 })
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	s.Unpublish("video", "agent", webrtc.RTPCodecTypeVideo)
	// Allow the already in-flight RTP packets to drain before checking restart.
	time.Sleep(100 * time.Millisecond)
	before := bv.Load()
	audioBeforeA, audioBeforeB := aa.Load(), ba.Load()
	start(a)
	wait("agent camera restarts and audio continues", func() bool {
		return bv.Load() > before+5 && aa.Load() > audioBeforeA+5 && ba.Load() > audioBeforeB+5 && len(rec.VideoTracks()) == 3
	})
	if audioSink.bad.Load() {
		t.Fatal("video packets leaked into an audio-only sink")
	}
	if len(rec.Tracks()) != 2 {
		t.Fatal("both microphones must be captured")
	}
}
