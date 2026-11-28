package sfu

import (
	"github.com/pion/webrtc/v4"

	"github.com/shridarpatil/tring/internal/media/room"
)

// One audio codec per room. The SFU never decodes audio on the forwarding
// path, so every publisher in a room must send the same codec: a phone room
// carries the carrier's G.711 end to end (FreeSWITCH then relays instead of
// transcoding to Opus), every other room carries Opus.

// SetRoomCodec sets the audio codec of roomID's browser mics: peers that Join
// afterwards are offered only that codec for their mic. Call it before anyone
// joins (a joined PC keeps the codec it negotiated) and clear it with
// room.Opus once the call ends.
func (s *SFU) SetRoomCodec(roomID room.RoomID, c room.Codec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == room.Opus {
		delete(s.codecs, roomID)
		return
	}
	s.codecs[roomID] = c
}

// RoomCodec returns roomID's audio codec (room.Opus unless SetRoomCodec said
// otherwise).
func (s *SFU) RoomCodec(roomID room.RoomID) room.Codec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codecs[roomID]
}

// AudioCapability is the track capability for publishing c into a room
// (InjectSource).
func AudioCapability(c room.Codec) webrtc.RTPCodecCapability {
	return audioParameters(c).RTPCodecCapability
}

// audioParameters is c as registered in the media engine (buildAPI).
func audioParameters(c room.Codec) webrtc.RTPCodecParameters {
	switch c {
	case room.PCMA:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000},
			PayloadType:        webrtc.PayloadType(c.PayloadType()),
		}
	case room.PCMU:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000},
			PayloadType:        webrtc.PayloadType(c.PayloadType()),
		}
	}
	// Standard WebRTC Opus fmtp: useinbandfec=1 lets the far side reconstruct
	// an isolated lost packet instead of hearing a gap; minptime=10 keeps 20ms
	// framing. maxplaybackrate/sprop-maxcapturerate=16000 ask browsers to encode
	// wideband (16 kHz, "HD voice") instead of fullband: plenty for speech, and
	// browsers then default to ~20 kbps instead of ~32, ~20% less per leg with
	// headers. The RTP clock stays 48 kHz (RFC 7587), and the SFU forwards Opus
	// untouched, so nothing else changes. pion matches answers only on params
	// both sides list, so a browser that echoes its own maxplaybackrate still
	// negotiates.
	return webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1;maxplaybackrate=16000;sprop-maxcapturerate=16000",
		},
		PayloadType: webrtc.PayloadType(room.Opus.PayloadType()),
	}
}
