//go:build integration

// Run with: go test -tags=integration ./internal/fsplug/...
//
// Requires the Tring FreeSWITCH (the CI compose stack) on this host: ESL on
// TRING_INTEGRATION_FS_ADDR / TRING_INTEGRATION_FS_PASSWORD (as
// internal/freeswitch's integration tests), and its tring-gw gateway pointing
// at TRING_INTEGRATION_PLUG_SIP (default 127.0.0.1:5090, where this test runs
// the plug). The fake carrier calls the external profile on the address
// FreeSWITCH reports over ESL.

package fsplug

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/freeswitch"
	"github.com/shridarpatil/tring/internal/media/room"
)

func intEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestCarrierCallRunsOpusAtCarrierRate: a G.711 carrier call through the real
// FreeSWITCH (public.xml → tring-gw) reaches the plug with X-Tring-Audio-Rate,
// FreeSWITCH runs the plug's Opus leg at 8 kHz instead of 48, and audio and the
// carrier's DTMF still get through.
func TestCarrierCallRunsOpusAtCarrierRate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plugSIP := intEnv("TRING_INTEGRATION_PLUG_SIP", "127.0.0.1:5090")
	plugIP, _, _ := net.SplitHostPort(plugSIP)
	esl := freeswitch.New(intEnv("TRING_INTEGRATION_FS_ADDR", "127.0.0.1:8021"), intEnv("TRING_INTEGRATION_FS_PASSWORD", "ClueCon"))
	// The external (carrier) profile binds the host's own address, not
	// loopback; the carrier sits on that address too.
	carrierIP := fsVar(ctx, t, esl, "tring_trunk_ip")
	fsSIP := carrierIP + ":" + fsVar(ctx, t, esl, "external_sip_port")

	// --- the plug, as the gateway runs it ---
	p, err := New(Config{SIPListen: plugSIP, SIPAdvertise: plugIP, RTPMin: 41000, RTPMax: 41100, OpusPT: 111, DTMFPT: 101}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	go p.Run(ctx)
	legs := make(chan *room.Leg, 1)
	p.OnInbound(func(ic room.InboundCall) {
		leg, err := p.Answer(ic.CallID)
		if err != nil {
			t.Errorf("Answer: %v", err)
			return
		}
		legs <- leg
	})

	// --- the carrier: G.711 + RFC 2833 into FreeSWITCH's public context ---
	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(carrierIP)})
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	offer := "v=0\r\no=carrier 1 1 IN IP4 " + carrierIP + "\r\ns=-\r\nc=IN IP4 " + carrierIP + "\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(media.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 0 101\r\n" +
		"a=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\na=ptime:20\r\n"
	ua, _ := sipgo.NewUA(sipgo.WithUserAgentHostname(carrierIP))
	client, _ := sipgo.NewClient(ua, sipgo.WithClientHostname(carrierIP))
	dc := sipgo.NewDialogClientCache(client, sip.ContactHeader{Address: sip.Uri{User: "carrier", Host: carrierIP}})
	host, port := splitHostPort(fsSIP)
	dlg, err := dc.Invite(ctx, sip.Uri{User: "912265915089", Host: host, Port: port}, []byte(offer),
		sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatalf("carrier INVITE: %v", err)
	}
	var leg *room.Leg
	select {
	case leg = <-legs:
	case <-ctx.Done():
		t.Fatal("the call never reached the plug")
	}
	if err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("carrier not answered: %v", err)
	}
	if err := dlg.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dlg.Bye(context.Background()) }()
	fsIP, fsPort, err := parseOffer(dlg.InviteResponse.Body())
	if err != nil {
		t.Fatal(err)
	}
	fsMedia := &net.UDPAddr{IP: net.ParseIP(fsIP), Port: fsPort}

	// FreeSWITCH's leg toward the plug runs Opus at the carrier's 8 kHz. `show
	// channels` reads FreeSWITCH's own DB, written asynchronously, so poll.
	var plugLeg *fsChannel
	for deadline := time.Now().Add(3 * time.Second); plugLeg == nil && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		plugLeg = opusChannel(ctx, t, esl)
	}
	if plugLeg == nil {
		t.Fatal("FreeSWITCH shows no Opus leg toward the plug")
	}
	t.Logf("plug leg %s: opus at %s Hz", plugLeg.Name, plugLeg.ReadRate)
	if plugLeg.ReadRate != "8000" {
		t.Errorf("FreeSWITCH runs the plug's Opus leg at %s Hz, want 8000", plugLeg.ReadRate)
	}

	// Audio both ways: carrier G.711 in → Opus at the plug; plug Opus in → G.711
	// at the carrier.
	carrierGot := make(chan int, 1)
	go func() {
		n := 0
		buf := make([]byte, 1500)
		_ = media.SetReadDeadline(time.Now().Add(3 * time.Second))
		for n < 10 {
			k, err := media.Read(buf)
			if err != nil {
				break
			}
			var pkt rtp.Packet
			if pkt.Unmarshal(buf[:k]) == nil && pkt.PayloadType == 0 {
				n++
			}
		}
		carrierGot <- n
	}()
	silence := make([]byte, 160)
	for i := range silence {
		silence[i] = 0xFF // μ-law silence
	}
	for i := 0; i < 25; i++ {
		send(t, media, fsMedia, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 1}, Payload: silence})
		_ = leg.Write(&rtp.Packet{Payload: []byte{0xF8, 0xFF, 0xFE}}) // 20 ms of Opus silence
		time.Sleep(20 * time.Millisecond)
	}
	plugGot := 0
	for plugGot < 10 {
		got := make(chan struct{})
		go func() { _, _ = leg.Source.ReadRTP(); close(got) }()
		select {
		case <-got:
			plugGot++
			continue
		case <-time.After(2 * time.Second):
		}
		break
	}
	if plugGot < 10 {
		t.Errorf("plug got %d Opus packets from the carrier's audio, want ≥10", plugGot)
	}
	if n := <-carrierGot; n < 10 {
		t.Errorf("carrier got %d G.711 packets from the plug's audio, want ≥10", n)
	}

	// The carrier's DTMF (RFC 2833 at 8 kHz) reaches the plug as a digit.
	for i := 0; i < 6; i++ {
		end := byte(0)
		if i >= 3 {
			end = 0x80
		}
		send(t, media, fsMedia, &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 101, Marker: i == 0, SequenceNumber: uint16(100 + i), Timestamp: 8000, SSRC: 1},
			Payload: []byte{5, end | 10, 0, byte(160 * (i + 1))},
		})
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case d := <-leg.DTMF:
		if d != "5" {
			t.Errorf("DTMF %q, want 5", d)
		}
	case <-time.After(3 * time.Second):
		t.Error("the carrier's DTMF never reached the plug")
	}
}

type fsChannel struct {
	Name      string `json:"name"`
	ReadCodec string `json:"read_codec"`
	ReadRate  string `json:"read_rate"`
}

// opusChannel returns FreeSWITCH's Opus channel (the leg toward the plug), or
// nil if it isn't listed yet.
func opusChannel(ctx context.Context, t *testing.T, esl *freeswitch.Client) *fsChannel {
	t.Helper()
	out, err := esl.Command(ctx, "show", "channels as json")
	if err != nil {
		t.Fatal(err)
	}
	var chans struct {
		Rows []fsChannel `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &chans); err != nil {
		t.Fatalf("show channels: %v\n%s", err, out)
	}
	for _, c := range chans.Rows {
		if c.ReadCodec == "opus" {
			return &c
		}
	}
	return nil
}

func fsVar(ctx context.Context, t *testing.T, esl *freeswitch.Client, name string) string {
	t.Helper()
	v, err := esl.Command(ctx, "global_getvar", name)
	if err != nil || v == "" {
		t.Fatalf("FreeSWITCH %s: %q, %v", name, v, err)
	}
	return v
}

func send(t *testing.T, conn *net.UDPConn, to *net.UDPAddr, pkt *rtp.Packet) {
	t.Helper()
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.WriteToUDP(raw, to); err != nil {
		t.Fatal(err)
	}
}
