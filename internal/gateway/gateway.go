// Package gateway is the browser-facing WebRTC media gateway (ADR-001). The
// browser peers with this gateway over raw WebRTC (DTLS-SRTP/Opus) with HTTPS
// offer/answer signaling — never with FreeSWITCH directly. The gateway
// terminates the browser leg and bridges media into a PRIVATE FreeSWITCH,
// which keeps doing IVR / trunk / masking / agent bridging.
//
// This file is the foundation (milestone 1): browser-leg termination + a
// loopback echo, so we can validate pion termination + signaling before the
// FreeSWITCH bridge (milestone 2) replaces the echo.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/cache"
	"github.com/whatomate/tring/internal/presence"
)

// Config controls ICE/media behaviour. ICE servers (STUN/TURN) are handed to
// the browser via the offer answer and used by the gateway's own candidates.
type Config struct {
	ICEServers []ICEServer
	// UDPPortMin/Max scope the media UDP port range so it's firewall-able.
	UDPPortMin, UDPPortMax uint16
	// PublicIP: advertise this as the host candidate (cloud/NAT). Empty in dev.
	PublicIP string
	// RelayOnly forces all media through TURN (no host candidates).
	RelayOnly bool
	// FS, when set, enables the FreeSWITCH bridge (the gateway SIP-dials FS).
	// nil keeps the gateway in standalone echo mode (milestone 1).
	FS *FSConfig
	// JWTSecret validates the agent token on /webrtc/offer (shared with the
	// API). RequireAuth gates whether a valid token is mandatory — kept off
	// during the rebuild so the echo test works token-less; flipped on once
	// the SDK sends tokens (M4/M5).
	JWTSecret   []byte
	RequireAuth bool
	// AgentRingTimeout bounds how long an inbound agent leg rings before the
	// gateway gives FS a 408 (the browser never accepted). Zero → 45s default.
	AgentRingTimeout time.Duration
	// Cache (Redis) backs agent presence so the API/orchestrator can see which
	// agents are connected. nil = presence disabled (no Redis).
	Cache  *cache.Cache
	Logger *slog.Logger
}

type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Gateway holds the shared pion API (media engine + setting engine) so every
// PeerConnection is built with the same codecs/port range.
type Gateway struct {
	cfg      Config
	api      *webrtc.API
	fs       *fsClient       // nil = standalone echo mode
	agents   *agentRegistry  // online agents' signaling WebSockets (M4b)
	presence *presence.Store // publishes agent presence to Redis (M5)
	tokens   jtiStore        // consumes one-time gateway-call-token jti (*cache.Cache in prod)
	log      *slog.Logger
}

// New builds a Gateway: registers Opus + telephone-event (DTMF), scopes the
// UDP port range, and applies the public-IP rewrite. Mirrors the proven pion
// v4 setup.
func New(cfg Config) (*Gateway, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register opus: %w", err)
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "audio/telephone-event", ClockRate: 8000},
		PayloadType:        101,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register telephone-event: %w", err)
	}

	se := webrtc.SettingEngine{}
	lo, hi := cfg.UDPPortMin, cfg.UDPPortMax
	if lo == 0 {
		lo = 10000
	}
	if hi == 0 {
		hi = 10100
	}
	if err := se.SetEphemeralUDPPortRange(lo, hi); err != nil {
		return nil, fmt.Errorf("udp port range: %w", err)
	}
	if cfg.PublicIP != "" && !cfg.RelayOnly {
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{cfg.PublicIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			return nil, fmt.Errorf("ice address rewrite: %w", err)
		}
	}

	pres := presence.New(cfg.Cache)
	g := &Gateway{
		cfg:      cfg,
		api:      webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)),
		agents:   newAgentRegistry(pres), // registry owns presence (aggregate per agent)
		presence: pres,
		tokens:   cfg.Cache, // Redis-backed; nil cache → single-use disabled (fail-open)
		log:      cfg.Logger.With("component", "gateway"),
	}
	if cfg.FS != nil {
		fc, err := newFSClient(*cfg.FS, g.log)
		if err != nil {
			return nil, fmt.Errorf("fs client: %w", err)
		}
		g.fs = fc
		// Register the UAS handlers (FS-delivered agent legs) BEFORE serving.
		g.registerFSUAS()
		fc.serve(context.Background())
		g.log.Info("freeswitch bridge enabled", "sip_addr", cfg.FS.SIPAddr, "target", cfg.FS.Target)
	}
	return g, nil
}

// TestFSLeg dials FreeSWITCH, reads inbound RTP for a few seconds, and reports
// the packet count, then hangs up. Headless verification of the SIP leg + media
// (milestone 2a) — no browser needed. Dev-only.
func (g *Gateway) TestFSLeg(ctx context.Context) (int, error) {
	if g.fs == nil {
		return 0, errors.New("FreeSWITCH bridge not configured")
	}
	leg, err := g.fs.dial(ctx, "", "") // default flow/target — dev check
	if err != nil {
		return 0, err
	}
	defer leg.Close()
	g.log.Info("fs leg up (echo test), reading inbound rtp for 4s", "fs_rtp", leg.FSRTPAddr)

	count := 0
	_ = leg.RTP.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 1500)
	for {
		n, _, err := leg.RTP.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if n > 0 {
			count++
		}
	}
	return count, nil
}

// newPeerConnection builds a PC with the configured ICE servers.
func (g *Gateway) newPeerConnection() (*webrtc.PeerConnection, error) {
	ice := make([]webrtc.ICEServer, 0, len(g.cfg.ICEServers))
	for _, s := range g.cfg.ICEServers {
		e := webrtc.ICEServer{URLs: s.URLs}
		if s.Username != "" {
			e.Username = s.Username
			e.Credential = s.Credential
			e.CredentialType = webrtc.ICECredentialTypePassword
		}
		ice = append(ice, e)
	}
	cfg := webrtc.Configuration{ICEServers: ice}
	if g.cfg.RelayOnly {
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	return g.api.NewPeerConnection(cfg)
}

// Echo answers a browser SDP offer with a loopback: whatever audio the browser
// sends is written straight back. Proves pion termination + Opus + signaling
// (milestone 1). Returns the answer SDP. The PeerConnection cleans itself up
// when the browser disconnects.
//
// Milestone 2 replaces the loopback body with the FreeSWITCH bridge: instead
// of writing the remote track back to the browser, forward its RTP into an
// FS call leg and pipe FS's RTP back to the browser track.
func (g *Gateway) Echo(ctx context.Context, offerSDP string) (string, error) {
	pc, err := g.newPeerConnection()
	if err != nil {
		return "", err
	}

	// Local track we send back to the browser — added before negotiation so
	// it's in the answer SDP.
	local, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "echo",
	)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("local track: %w", err)
	}
	if _, err := pc.AddTrack(local); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("add track: %w", err)
	}

	// Loopback: copy the browser's RTP straight to the local track.
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		buf := make([]byte, 1500)
		for {
			n, _, readErr := remote.Read(buf)
			if readErr != nil {
				return
			}
			if _, writeErr := local.Write(buf[:n]); writeErr != nil && !errors.Is(writeErr, io.ErrClosedPipe) {
				return
			}
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		g.log.Debug("pc state", "state", s.String())
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			_ = pc.Close()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: offerSDP,
	}); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("set remote: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("set local: %w", err)
	}

	// Non-trickle: wait for ICE gathering so the answer carries all candidates.
	local2, err := g.waitForICE(pc, 10*time.Second)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	return local2.SDP, nil
}

// Bridge answers a browser SDP offer and bridges its audio to a FreeSWITCH
// call leg (the real path, milestone 2b): browser DTLS-SRTP/Opus is terminated
// here and relayed as plain RTP to FS, and FS's RTP is relayed back to the
// browser. Opus passes through (no transcoding) since both legs negotiate it.
// Returns the answer SDP. Falls back to Echo if FS isn't configured.
func (g *Gateway) Bridge(ctx context.Context, offerSDP, flowID, clientRef string) (string, error) {
	if g.fs == nil {
		return g.Echo(ctx, offerSDP)
	}
	pc, toBrowser, err := g.newBridgePC()
	if err != nil {
		return "", err
	}

	// Establish the FreeSWITCH leg up front so it's ready before we answer.
	leg, err := g.fs.dial(ctx, flowID, clientRef)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("fs dial: %w", err)
	}
	g.log.Info("bridge: fs leg up", "fs_rtp", leg.FSRTPAddr)

	// Visitor leg: no media-inactivity backstop — IVR think-time has legitimate
	// RTP gaps. Teardown comes from the dialog BYE watcher / pc state / DC bye.
	teardown := g.relay(pc, toBrowser, leg, 0, nil)

	// When the IVR/flow ends, FreeSWITCH hangs up the visitor leg with a SIP
	// BYE (routed to our client dialog by OnBye in fsuas.go). The RTP socket
	// and the browser pc both stay open, so nothing in the relay notices —
	// watch the dialog and tear the bridge down when it ends, or the browser
	// keeps streaming to a dead leg ("client call still running").
	if leg.session != nil {
		go func() {
			for st := range leg.session.StateRead() {
				if st == sip.DialogStateEnded {
					g.log.Info("visitor leg ended (fs bye), tearing down bridge")
					teardown()
					return
				}
			}
		}()
	}

	answer, err := g.negotiateAnswer(pc, offerSDP)
	if err != nil {
		teardown()
		return "", err
	}
	return answer, nil
}

// newBridgePC builds a PeerConnection with one outbound Opus track that the
// gateway writes FS audio onto (added before negotiation). Shared by the
// visitor (UAC) and agent (UAS) bridge paths.
func (g *Gateway) newBridgePC() (*webrtc.PeerConnection, *webrtc.TrackLocalStaticRTP, error) {
	pc, err := g.newPeerConnection()
	if err != nil {
		return nil, nil, err
	}
	toBrowser, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "fs")
	if err != nil {
		_ = pc.Close()
		return nil, nil, fmt.Errorf("local track: %w", err)
	}
	if _, err := pc.AddTrack(toBrowser); err != nil {
		_ = pc.Close()
		return nil, nil, fmt.Errorf("add track: %w", err)
	}
	return pc, toBrowser, nil
}

// relay wires the bidirectional media bridge between a browser PeerConnection
// and an FS RTP leg and returns an idempotent teardown. onClose, if non-nil,
// runs once inside teardown (after the pc/leg are closed) — the agent path uses
// it to BYE the SIP dialog and notify the browser. The caller negotiates the pc
// (SetRemote/Local) afterwards via negotiateAnswer.
// agentMediaTimeout: if the agent leg gets no RTP from FS for this long after
// media has started, tear it down. FS stops sending the agent leg's RTP the
// instant the bridge ends (the visitor hung up), so a stall here means the call
// is over even when we never get/match a clean SIP BYE — without this backstop
// the agent's screen sticks on "connected". The visitor leg passes 0 (disabled)
// because IVR think-time legitimately has >timeout RTP gaps.
const agentMediaTimeout = 8 * time.Second

func (g *Gateway) relay(pc *webrtc.PeerConnection, toBrowser *webrtc.TrackLocalStaticRTP, leg *fsLeg, mediaTimeout time.Duration, onClose func()) func() {
	var (
		once          sync.Once
		stop          = make(chan struct{})
		toFSPkts      atomic.Int64
		toBrowserPkts atomic.Int64
		byeDC         atomic.Pointer[webrtc.DataChannel]
	)
	teardown := func() {
		once.Do(func() {
			close(stop)
			// Best-effort end signal before we tear the transport down. Only
			// when the channel is actually open (a server-initiated teardown
			// while the browser is still connected); the browser keeps the
			// ICE/DTLS fallback, so a dropped "bye" just reverts to the slow path.
			if dc := byeDC.Load(); dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
				_ = dc.SendText("bye")
				time.Sleep(150 * time.Millisecond) // let SCTP flush before close
			}
			leg.Close()
			_ = pc.Close()
			if onClose != nil {
				onClose()
			}
		})
	}

	// The browser opens a "tring" DataChannel (SCTP muxed over the same DTLS
	// transport/port as the audio — no extra port). It's bidirectional:
	//   • gateway → browser "bye" (in teardown above): the browser ends its call
	//     UI immediately instead of waiting on its ICE/DTLS disconnect timer.
	//   • browser → gateway "bye" (here): the visitor hanging up tells us to tear
	//     down the FS leg now, so FS hangs up the bridged agent promptly — without
	//     it the agent leg lingers until OUR visitor pc hits the disconnect timer.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "tring" {
			return
		}
		byeDC.Store(dc)
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if string(msg.Data) == "bye" {
				g.log.Info("browser sent bye, tearing down bridge")
				teardown()
			}
		})
	})

	// Diagnostic: log per-direction packet counts every 2s so we can see which
	// leg of the relay is (or isn't) flowing.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				g.log.Debug("relay packet counts",
					"browser_to_fs", toFSPkts.Load(), "fs_to_browser", toBrowserPkts.Load())
			}
		}
	}()

	// FS → browser: relay FS's RTP to the browser track. (StaticRTP.Write
	// rewrites SSRC/PT to the negotiated values per receiver.)
	go func() {
		buf := make([]byte, 1500)
		gotFirst := false // arm the inactivity deadline only once media is flowing
		for {
			// mediaTimeout>0 (agent leg): after the first packet, a read that
			// blocks longer than the timeout means FS stopped sending — the
			// bridge is gone — so we tear down even absent a clean SIP BYE. No
			// deadline before the first packet, so slow ICE/bridge setup is fine.
			if mediaTimeout > 0 && gotFirst {
				_ = leg.RTP.SetReadDeadline(time.Now().Add(mediaTimeout))
			}
			n, _, err := leg.RTP.ReadFromUDP(buf)
			if err != nil {
				teardown()
				return
			}
			gotFirst = true
			toBrowserPkts.Add(1)
			if _, err := toBrowser.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// browser → FS: re-packetize into CLEAN RTP. The browser's WebRTC RTP
	// carries header extensions (audio-level, mid, abs-send-time…) negotiated
	// with us but NOT with FreeSWITCH's plain-RTP leg — FS drops packets it
	// can't reconcile, so it never echoes. Stripping the extensions and
	// re-marshalling gives FS a clean Opus stream (PT stays 111, matches).
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		g.log.Debug("browser track arrived", "codec", remote.Codec().MimeType)
		// The browser sends two payload types on the audio track: Opus (its
		// negotiated PT — Firefox 109, Chrome 111) and telephone-event for
		// DTMF (another dynamic PT). FS rejects any PT that isn't the one IT
		// negotiated, so we must remap PER CODEC: Opus → FS's Opus PT, DTMF →
		// FS's telephone-event PT. A blanket rewrite to Opus would turn DTMF
		// into noise and IVR menus wouldn't register key presses. Browser
		// seq/timestamp/SSRC are valid WebRTC — keep them; just strip the
		// extensions FS didn't negotiate and fix the PT.
		browserOpusPT := uint8(remote.Codec().PayloadType)
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			pkt.Header.Extension = false
			pkt.Header.ExtensionProfile = 0
			pkt.Header.Extensions = nil
			if uint8(pkt.Header.PayloadType) == browserOpusPT {
				pkt.Header.PayloadType = leg.OpusPT
			} else {
				// Anything else on the audio track is telephone-event (DTMF).
				pkt.Header.PayloadType = leg.DTMFPT
			}
			raw, err := pkt.Marshal()
			if err != nil {
				continue
			}
			toFSPkts.Add(1)
			if _, err := leg.RTP.WriteToUDP(raw, leg.FSRTPAddr); err != nil {
				return
			}
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		g.log.Debug("bridge pc state", "state", s.String())
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			teardown()
		}
	})
	return teardown
}

// negotiateAnswer applies the browser's offer to pc and returns the gateway's
// answer SDP once ICE gathering completes.
func (g *Gateway) negotiateAnswer(pc *webrtc.PeerConnection, offerSDP string) (string, error) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return "", fmt.Errorf("set remote: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local: %w", err)
	}
	desc, err := g.waitForICE(pc, 10*time.Second)
	if err != nil {
		return "", err
	}
	return desc.SDP, nil
}

func (g *Gateway) waitForICE(pc *webrtc.PeerConnection, timeout time.Duration) (*webrtc.SessionDescription, error) {
	done := webrtc.GatheringCompletePromise(pc)
	select {
	case <-done:
	case <-time.After(timeout):
		return nil, errors.New("ICE gathering timed out")
	}
	if d := pc.LocalDescription(); d != nil {
		return d, nil
	}
	return nil, errors.New("no local description")
}
