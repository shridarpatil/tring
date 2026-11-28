package sfu_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/shridarpatil/tring/internal/media/room"
	"github.com/shridarpatil/tring/internal/media/sfu"
)

// offerSink captures the server's offers without answering them.
type offerSink struct {
	mu     sync.Mutex
	offers []string
}

func (o *offerSink) SendOffer(sdp string) error {
	o.mu.Lock()
	o.offers = append(o.offers, sdp)
	o.mu.Unlock()
	return nil
}
func (o *offerSink) SendCandidate(webrtc.ICECandidateInit) error { return nil }

// micFormats joins a peer to roomID and returns the payload types the server
// offers for its mic (the first audio m-line), in offer order.
func micFormats(t *testing.T, s *sfu.SFU, roomID room.RoomID) []string {
	t.Helper()
	sig := &offerSink{}
	p, err := s.Join(roomID, "agent", sig)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	t.Cleanup(func() { s.Leave(roomID, "agent") })
	p.Negotiate()
	sig.mu.Lock()
	defer sig.mu.Unlock()
	if len(sig.offers) == 0 {
		t.Fatal("no offer sent")
	}
	var desc sdp.SessionDescription
	if err := desc.Unmarshal([]byte(sig.offers[0])); err != nil {
		t.Fatalf("parse offer: %v", err)
	}
	for _, md := range desc.MediaDescriptions {
		if md.MediaName.Media == "audio" {
			return md.MediaName.Formats
		}
	}
	t.Fatal("offer has no audio m-line")
	return nil
}

// A web room still offers Opus first: registering G.711 must not move web
// calls off Opus.
func TestWebRoomOffersOpusFirst(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(micFormats(t, s, "web"), " ")
	if got != "111 8 0" {
		t.Fatalf("mic formats = %q, want Opus then PCMA then PCMU (111 8 0)", got)
	}
}

// A phone room offers the mic only its law, so the browser must answer it.
// Web calls ask browsers for wideband (16 kHz) Opus: fullband buys nothing for
// speech and costs ~12 kbps more per leg.
func TestWebRoomCapsOpusAt16kHz(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sig := &offerSink{}
	p, err := s.Join("web16", "agent", sig)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	t.Cleanup(func() { s.Leave("web16", "agent") })
	p.Negotiate()
	sig.mu.Lock()
	defer sig.mu.Unlock()
	if len(sig.offers) == 0 {
		t.Fatal("no offer sent")
	}
	if want := "a=fmtp:111 minptime=10;useinbandfec=1;maxplaybackrate=16000;sprop-maxcapturerate=16000"; !strings.Contains(sig.offers[0], want) {
		t.Fatalf("offer lacks %q:\n%s", want, sig.offers[0])
	}
}

func TestPhoneRoomOffersOnlyItsLaw(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for c, want := range map[room.Codec]string{room.PCMA: "8", room.PCMU: "0"} {
		id := room.RoomID("phone-" + string(c))
		s.SetRoomCodec(id, c)
		if got := strings.Join(micFormats(t, s, id), " "); got != want {
			t.Fatalf("%s room: mic formats = %q, want %q", c, got, want)
		}
		s.SetRoomCodec(id, room.Opus)
		if s.RoomCodec(id) != room.Opus {
			t.Fatalf("%s room: codec not cleared", c)
		}
	}
}

// Two browsers in a PCMA room: the mic negotiates PCMA and the SFU forwards it
// to the other browser as PCMA, untouched.
func TestPhoneRoomForwardsPCMABetweenBrowsers(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.SetRoomCodec("call", room.PCMA)
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	a := newClient(t, s, clientAPI(t), "call", "a", &closers)
	b := newClient(t, s, clientAPI(t), "call", "b", &closers)

	got := make(chan webrtc.RTPCodecParameters, 1)
	b.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			select {
			case got <- tr.Codec():
			default:
			}
		}
	})
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000}, "audio", "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pc.AddTrack(track); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		frame := make([]byte, 160)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				_ = track.WriteSample(media.Sample{Data: frame, Duration: 20 * time.Millisecond})
			}
		}
	}()
	a.connect()
	b.connect()

	select {
	case c := <-got:
		if c.MimeType != webrtc.MimeTypePCMA || c.PayloadType != 8 {
			t.Fatalf("b received %s PT %d, want PCMA PT 8", c.MimeType, c.PayloadType)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("b received no audio")
	}
}

// ptSource is a slowSource that sends payload type pt.
type ptSource struct {
	*slowSource
	pt uint8
}

func (p ptSource) ReadRTP() (*rtp.Packet, error) {
	pkt, err := p.slowSource.ReadRTP()
	if pkt != nil {
		pkt.PayloadType = p.pt
	}
	return pkt, err
}

// A G.711 port steps its timestamps on the 8 kHz clock and never forwards a
// frame in another codec (it would play as noise).
func TestPCMAPortSteps160AndDropsOtherCodecs(t *testing.T) {
	s, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.SetRoomCodec("call", room.PCMA)
	out := &pktCollector{}
	leg := ptSource{&slowSource{id: "phone-leg", mark: 0xCC, total: 200}, 8}
	if err := s.AttachPort("call", leg, room.PCMA, out.write); err != nil {
		t.Fatal(err)
	}
	stray := ptSource{&slowSource{id: "opus", mark: 0xBB, total: 30}, 111}
	if err := s.InjectSource("call", stray, opusCap()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // the stray source runs out
	if out.count() != 0 {
		t.Fatalf("port forwarded %d Opus packets to a PCMA leg", out.count())
	}

	agent := ptSource{&slowSource{id: "agent", mark: 0xAA, total: 50}, 8}
	if err := s.InjectSource("call", agent, sfu.AudioCapability(room.PCMA)); err != nil {
		t.Fatal(err)
	}
	waitCount(t, out, 10, "port hears the PCMA agent")
	out.mu.Lock()
	defer out.mu.Unlock()
	for i := 1; i < len(out.pkts); i++ {
		if d := out.pkts[i].Timestamp - out.pkts[i-1].Timestamp; d != 160 {
			t.Fatalf("timestamp step = %d, want 160", d)
		}
		if out.pkts[i].PayloadType != 8 {
			t.Fatalf("payload type = %d, want 8", out.pkts[i].PayloadType)
		}
	}
}
