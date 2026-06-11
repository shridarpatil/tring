// Package sfu is the pion media anchor for Tring's media plane: every browser
// (and every externally-fed participant) holds exactly one PeerConnection to
// this SFU, and the SFU forwards each participant's audio/video tracks to the
// others. It is the only package in internal/media that imports pion/webrtc.
//
// Forwarding uses a room-owned fan-out track per publisher: the publisher's
// remote RTP is copied into a TrackLocalStaticRTP that every other peer
// subscribes to. The fan-out track is keyed by the stable room.ParticipantID,
// not the PeerConnection — so when a participant reconnects with a FRESH
// PeerConnection (M4 resumption), we re-point the copy loop into the same
// fan-out track and subscribers never renegotiate. The SFU carries Opus (audio)
// and VP8/VP9 (screen share, M5); DTMF rides the data channel, not RFC 4733, so
// telephone-event is intentionally NOT registered.
package sfu

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/whatomate/tring/internal/media/room"
	"github.com/whatomate/tring/internal/turn"
)

// ICEServer is a STUN/TURN entry handed to pion for the SFU's own candidate
// gathering (the signaling layer issues the browser its own list).
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Config controls the SFU's media engine and ICE behaviour. It mirrors the
// proven internal/gateway setup so the two media surfaces can't drift on codecs
// or NAT handling.
type Config struct {
	ICEServers []ICEServer
	// UDPPortMin/Max scope the media UDP range so it's firewall-able.
	UDPPortMin, UDPPortMax uint16
	// PublicIP rewrites the host ICE candidate to a browser-reachable address
	// (cloud/NAT). Empty in dev (loopback candidates).
	PublicIP string
	// RelayOnly forces all media through TURN (no host candidates).
	RelayOnly bool
	// TURNURL+TURNSecret make the SFU a TURN client: it mints an ephemeral coturn
	// credential per PeerConnection so it gathers its own relay candidate (needed
	// for RelayOnly and for browsers that can only reach us via TURN).
	TURNURL    string
	TURNSecret string
	TURNTTL    time.Duration
	// IncludeLoopback adds 127.0.0.1 ICE candidates. Off in production (host /
	// PublicIP candidates are used); on for same-host dev and tests so local ICE
	// is deterministic without depending on a routable interface.
	IncludeLoopback bool
	Logger          *slog.Logger
}

// SFU holds the pion media/setting-engine config and the live peers grouped by
// room. A fresh *webrtc.API is built per PeerConnection (buildAPI) rather than
// shared, so concurrent ICE/DTLS on multiple peers can't contend on one engine.
type SFU struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	rooms map[room.RoomID]*sfuRoom
}

// sfuRoom holds a room's live peers plus its publications. A publication (a
// participant's fan-out tracks) is keyed by participant id and OUTLIVES the
// peer's PeerConnection: on reconnect the new PC re-points its copy loop into
// the same fan-out track, so subscribers never renegotiate (M4 resumption).
type sfuRoom struct {
	peers map[room.ParticipantID]*Peer
	pubs  map[room.ParticipantID]*publication
}

// publication is one participant's fan-out tracks, one per codec kind. The
// tracks are created lazily on first publish and reused across reconnects.
// sinks are downstream consumers (recording, STT, an IVR's ear) that get a copy
// of every RTP packet teed off the copy loop; stored atomically so the hot loop
// reads them without locking.
type publication struct {
	tracks map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP
	sinks  atomic.Pointer[[]room.TrackSink]
}

// New builds an SFU. It validates the media config by building one API up front
// (so a bad codec/port config fails fast), then builds a fresh API per
// PeerConnection at Join time.
func New(cfg Config) (*SFU, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &SFU{
		cfg:   cfg,
		log:   cfg.Logger.With("component", "sfu"),
		rooms: make(map[room.RoomID]*sfuRoom),
	}
	if _, err := s.buildAPI(); err != nil {
		return nil, err
	}
	return s, nil
}

// buildAPI constructs a fresh pion API: Opus + VP8/VP9 codecs, the UDP port
// range (when configured), mDNS disabled, and the public-IP host-candidate
// rewrite. A new one is used per PeerConnection.
func (s *SFU) buildAPI() (*webrtc.API, error) {
	cfg := s.cfg
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		// Standard WebRTC Opus fmtp: useinbandfec=1 lets the far side reconstruct
		// an isolated lost packet instead of hearing a gap; minptime=10 keeps 20ms
		// framing. stereo=1 so screen-share/music isn't downmixed.
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register opus: %w", err)
	}

	// Video for screen share (M5). NACK+PLI feedback so the SFU can request a
	// keyframe when a subscriber joins mid-stream.
	videoFB := []webrtc.RTCPFeedback{
		{Type: webrtc.TypeRTCPFBNACK},
		{Type: webrtc.TypeRTCPFBNACK, Parameter: "pli"},
	}
	for _, c := range []webrtc.RTPCodecParameters{
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: videoFB}, PayloadType: 96},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=0", RTCPFeedback: videoFB}, PayloadType: 98},
	} {
		if err := me.RegisterCodec(c, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, fmt.Errorf("register %s: %w", c.MimeType, err)
		}
	}

	se := webrtc.SettingEngine{}
	// Bound the media UDP range when configured (firewall-able). Both zero =
	// leave it unrestricted (OS ephemeral ports) — used by tests so rapid
	// PC churn doesn't exhaust a narrow range. cmd/gateway always sets a range.
	if cfg.UDPPortMin != 0 || cfg.UDPPortMax != 0 {
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
	}
	// No mDNS: a media anchor advertises real host / PublicIP candidates, and
	// .local candidates only slow gathering (and flake local ICE in tests).
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	if cfg.IncludeLoopback {
		se.SetIncludeLoopbackCandidate(true)
	}
	if cfg.PublicIP != "" && !cfg.RelayOnly {
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{cfg.PublicIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			return nil, fmt.Errorf("ice address rewrite: %w", err)
		}
	}

	return webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)), nil
}

// newPeerConnection builds a PC with the configured ICE servers, minting a fresh
// ephemeral TURN credential when a shared secret is set (so the SFU gathers its
// own relay candidate). Mirrors internal/gateway.newPeerConnection.
func (s *SFU) newPeerConnection() (*webrtc.PeerConnection, error) {
	ice := make([]webrtc.ICEServer, 0, len(s.cfg.ICEServers)+1)
	for _, e := range s.cfg.ICEServers {
		srv := webrtc.ICEServer{URLs: e.URLs}
		if e.Username != "" {
			srv.Username = e.Username
			srv.Credential = e.Credential
			srv.CredentialType = webrtc.ICECredentialTypePassword
		}
		ice = append(ice, srv)
	}
	if s.cfg.TURNSecret != "" && s.cfg.TURNURL != "" {
		ttl := s.cfg.TURNTTL
		if ttl <= 0 {
			ttl = 4 * time.Hour
		}
		user, cred := turn.Credential(s.cfg.TURNSecret, "sfu", ttl)
		ice = append(ice, webrtc.ICEServer{
			URLs:           []string{s.cfg.TURNURL},
			Username:       user,
			Credential:     cred,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	conf := webrtc.Configuration{ICEServers: ice}
	if s.cfg.RelayOnly {
		conf.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	api, err := s.buildAPI()
	if err != nil {
		return nil, err
	}
	return api.NewPeerConnection(conf)
}
