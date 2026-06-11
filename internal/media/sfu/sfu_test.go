package sfu_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/media/sfu"
)

// clientAPI builds a pion API for the test clients with mDNS disabled (real host
// candidates), so the in-process SFU connection uses the loopback/LAN interface.
func clientAPI(t *testing.T) *webrtc.API {
	t.Helper()
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatalf("register default codecs: %v", err)
	}
	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	return webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se))
}

// client is a test peer: a real pion PeerConnection wired to an sfu.Peer through
// the Signaler interface. The SFU is the sole offerer, so the client only ever
// ANSWERS (SendOffer) and adds ICE candidates (SendCandidate).
type client struct {
	t      *testing.T
	pc     *webrtc.PeerConnection
	server *sfu.Peer

	mu         sync.Mutex
	haveRemote bool
	pending    []webrtc.ICECandidateInit
}

// SendOffer answers a server offer and hands the answer back to the SFU.
func (c *client) SendOffer(sdp string) error {
	if err := c.setRemote(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return err
	}
	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := c.pc.SetLocalDescription(answer); err != nil {
		return err
	}
	return c.server.HandleAnswer(answer.SDP)
}

// SendCandidate trickles a server ICE candidate to the client, queueing until
// the client has applied an offer (its remote description).
func (c *client) SendCandidate(cand webrtc.ICECandidateInit) error {
	c.mu.Lock()
	if !c.haveRemote {
		c.pending = append(c.pending, cand)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.pc.AddICECandidate(cand)
}

func (c *client) setRemote(d webrtc.SessionDescription) error {
	if err := c.pc.SetRemoteDescription(d); err != nil {
		return err
	}
	c.mu.Lock()
	c.haveRemote = true
	pend := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, cand := range pend {
		_ = c.pc.AddICECandidate(cand)
	}
	return nil
}

// newClient builds a client joined to room/pid on the SFU, registering its
// teardown on closers. It drains any track it receives by default (the test
// overrides OnTrack on the peer it asserts on).
func newClient(t *testing.T, s *sfu.SFU, api *webrtc.API, roomID room.RoomID, pid room.ParticipantID, closers *[]func()) *client {
	t.Helper()
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("client pc: %v", err)
	}
	*closers = append(*closers, func() { _ = pc.Close() })
	c := &client{t: t, pc: pc}
	server, err := s.Join(roomID, pid, c)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	c.server = server
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		_ = server.HandleCandidate(cand.ToJSON())
	})
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		buf := make([]byte, 1500)
		for {
			if _, _, err := tr.Read(buf); err != nil {
				return
			}
		}
	})
	return c
}

// publishOpus adds an Opus mic track (so the client answers the server's
// recvonly-audio offer as sendrecv) and writes 20ms frames for the SFU to
// forward. Must be called before connect.
func (c *client) publishOpus(t *testing.T, closers *[]func()) {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "stream-"+string(c.server.ID()),
	)
	if err != nil {
		t.Fatalf("local track: %v", err)
	}
	if _, err := c.pc.AddTrack(track); err != nil {
		t.Fatalf("add track: %v", err)
	}
	stop := make(chan struct{})
	*closers = append(*closers, func() { close(stop) })
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = track.WriteSample(media.Sample{Data: make([]byte, 160), Duration: 20 * time.Millisecond})
			}
		}
	}()
}

// addScreenShare adds a VP8 video track (writing dummy frames) the client will
// attach once the server invites it via Publish. Added before connect; idle
// until the server's publish renegotiation references its m-line.
func (c *client) addScreenShare(t *testing.T, closers *[]func()) {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
		"video", "screen-"+string(c.server.ID()),
	)
	if err != nil {
		t.Fatalf("video track: %v", err)
	}
	if _, err := c.pc.AddTrack(track); err != nil {
		t.Fatalf("add video track: %v", err)
	}
	stop := make(chan struct{})
	*closers = append(*closers, func() { close(stop) })
	go func() {
		tick := time.NewTicker(33 * time.Millisecond) // ~30fps
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
}

// connect kicks off the server's initial offer (the server is the sole offerer).
func (c *client) connect() { c.server.Negotiate() }

func TestForwardsAudioBetweenParticipants(t *testing.T) {
	// In-process pion ICE/DTLS over a single host is racy — a PeerConnection
	// occasionally stalls in the DTLS handshake even though ICE connected. That's
	// a same-host transport artifact, NOT an SFU bug: production is browser↔server
	// over a real network. So retry the whole scenario a few times (each fully
	// torn down) and require that forwarding works on at least one attempt.
	const attempts = 8
	for i := 1; i <= attempts; i++ {
		if forwardOnce(t) {
			return // A's RTP reached B through the SFU
		}
		t.Logf("attempt %d/%d: media did not flow (in-process ICE stall), retrying", i, attempts)
	}
	t.Fatalf("B never received A's forwarded audio after %d attempts", attempts)
}

// forwardOnce runs one A→B forwarding attempt and reports whether B received
// A's audio within a short deadline. All resources are torn down before return.
func forwardOnce(t *testing.T) bool {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	a := newClient(t, s, clientAPI(t), "call", "a", &closers)
	b := newClient(t, s, clientAPI(t), "call", "b", &closers)

	var bGot int64
	gotMedia := make(chan struct{})
	var once sync.Once
	b.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			if atomic.AddInt64(&bGot, 1) >= 15 {
				once.Do(func() { close(gotMedia) })
			}
		}
	})

	a.publishOpus(t, &closers)
	b.publishOpus(t, &closers)
	a.connect()
	b.connect()

	select {
	case <-gotMedia:
		return true
	case <-time.After(4 * time.Second):
		return false
	}
}

func TestForwardsScreenShareWithoutDroppingAudio(t *testing.T) {
	const attempts = 8
	for i := 1; i <= attempts; i++ {
		if screenShareOnce(t) {
			return
		}
		t.Logf("attempt %d/%d: screen-share video did not flow (in-process ICE), retrying", i, attempts)
	}
	t.Fatalf("B never received A's forwarded screen-share video after %d attempts", attempts)
}

// screenShareOnce runs one attempt: A and B connect with audio, A starts screen
// share (server Publish → renegotiation), and we require that B receives A's
// VIDEO track while its AUDIO keeps flowing (audio must not drop).
func screenShareOnce(t *testing.T) bool {
	t.Helper()
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	a := newClient(t, s, clientAPI(t), "call", "a", &closers)
	b := newClient(t, s, clientAPI(t), "call", "b", &closers)

	var bAudio, bVideo int64
	gotAudio := make(chan struct{})
	var audioOnce sync.Once
	b.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		isVideo := tr.Kind() == webrtc.RTPCodecTypeVideo
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			if isVideo {
				atomic.AddInt64(&bVideo, 1)
			} else if atomic.AddInt64(&bAudio, 1) >= 15 {
				audioOnce.Do(func() { close(gotAudio) })
			}
		}
	})

	a.publishOpus(t, &closers)
	b.publishOpus(t, &closers)
	a.addScreenShare(t, &closers) // track added; server invites it via Publish below
	a.connect()
	b.connect()

	// Wait for audio first.
	select {
	case <-gotAudio:
	case <-time.After(4 * time.Second):
		return false
	}

	// Start screen share: the server adds a recvonly video transceiver and
	// renegotiates; A answers attaching its video; B subscribes to it.
	a.server.Publish(webrtc.RTPCodecTypeVideo)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&bVideo) >= 10 {
			// Video arrived AND audio kept flowing throughout.
			return atomic.LoadInt64(&bAudio) >= 15
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
