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
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
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
	Logger      *log.Logger
}

type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Gateway holds the shared pion API (media engine + setting engine) so every
// PeerConnection is built with the same codecs/port range.
type Gateway struct {
	cfg Config
	api *webrtc.API
	fs  *fsClient // nil = standalone echo mode
	log *log.Logger
}

// authOffer enforces the offer-endpoint auth policy. Returns ok=false (and
// writes the 401) when a token is required but missing/invalid. When auth is
// disabled it's a no-op pass.
func (g *Gateway) authOffer(w http.ResponseWriter, r *http.Request) bool {
	if !g.cfg.RequireAuth {
		return true
	}
	claims, err := validateToken(bearer(r), g.cfg.JWTSecret)
	if err != nil {
		http.Error(w, `{"error":"invalid or missing token"}`, http.StatusUnauthorized)
		return false
	}
	g.log.Printf("gateway: authed offer tenant=%s sip_user=%s", claims.TenantID, claims.SipUsername)
	return true
}

// New builds a Gateway: registers Opus + telephone-event (DTMF), scopes the
// UDP port range, and applies the public-IP rewrite. Mirrors the proven pion
// v4 setup.
func New(cfg Config) (*Gateway, error) {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
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

	g := &Gateway{
		cfg: cfg,
		api: webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)),
		log: cfg.Logger,
	}
	if cfg.FS != nil {
		fc, err := newFSClient(context.Background(), *cfg.FS, g.log)
		if err != nil {
			return nil, fmt.Errorf("fs client: %w", err)
		}
		g.fs = fc
		g.log.Printf("gateway: FreeSWITCH bridge enabled → %s (target %s)", cfg.FS.SIPAddr, cfg.FS.Target)
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
	leg, err := g.fs.dial(ctx)
	if err != nil {
		return 0, err
	}
	defer leg.Close()
	g.log.Printf("gateway: FS leg up, FS RTP at %s — reading inbound RTP for 4s", leg.FSRTPAddr)

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
		g.log.Printf("gateway: pc state=%s", s)
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
func (g *Gateway) Bridge(ctx context.Context, offerSDP string) (string, error) {
	if g.fs == nil {
		return g.Echo(ctx, offerSDP)
	}
	pc, err := g.newPeerConnection()
	if err != nil {
		return "", err
	}

	// Track we send FS's audio to the browser on (added before negotiation).
	toBrowser, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "fs")
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("local track: %w", err)
	}
	if _, err := pc.AddTrack(toBrowser); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("add track: %w", err)
	}

	// Establish the FreeSWITCH leg up front so it's ready before we answer.
	leg, err := g.fs.dial(ctx)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("fs dial: %w", err)
	}
	g.log.Printf("gateway: bridge — FS leg up (FS RTP %s)", leg.FSRTPAddr)

	var (
		once          sync.Once
		stop          = make(chan struct{})
		toFSPkts      atomic.Int64
		toBrowserPkts atomic.Int64
	)
	teardown := func() {
		once.Do(func() {
			close(stop)
			leg.Close()
			_ = pc.Close()
		})
	}

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
				g.log.Printf("gateway: relay pkts browser→FS=%d FS→browser=%d",
					toFSPkts.Load(), toBrowserPkts.Load())
			}
		}
	}()

	// FS → browser: relay FS's RTP to the browser track. (StaticRTP.Write
	// rewrites SSRC/PT to the negotiated values per receiver.)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := leg.RTP.ReadFromUDP(buf)
			if err != nil {
				teardown()
				return
			}
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
		g.log.Printf("gateway: bridge — browser track arrived (%s)", remote.Codec().MimeType)
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
		g.log.Printf("gateway: bridge pc state=%s", s)
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			teardown()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		teardown()
		return "", fmt.Errorf("set remote: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		teardown()
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		teardown()
		return "", fmt.Errorf("set local: %w", err)
	}
	desc, err := g.waitForICE(pc, 10*time.Second)
	if err != nil {
		teardown()
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
