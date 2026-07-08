package room

import (
	"context"

	"github.com/pion/rtp"
)

// The extension interfaces below are the seams that keep every channel's
// participants indistinguishable to the room. The core defines them and stays
// free of any channel-specific concept (FreeSWITCH/SIP, WhatsApp, WebRTC) — that
// is what lets each channel be a swappable Plug adapter. See
// docs/superpowers/specs/2026-06-24-plug-architecture-design.md.

// ParticipantID identifies a participant within its room. Stable across a
// transport drop + reattach, so the slot (and the peer's subscription to it)
// survives a refresh.
type ParticipantID string

// RoomID identifies a call/room.
type RoomID string

// CallID identifies a channel-side call leg (Plug scope), distinct from the
// room-side ParticipantID it maps to.
type CallID string

// TrackSink receives a participant's RTP for a downstream consumer — call
// recording and the Sarvam STT tap (Phase 3) implement it. The room tees each
// publisher's packets to any attached sinks; a sink never affects forwarding.
type TrackSink interface {
	WriteRTP(p ParticipantID, pkt *rtp.Packet) error
	Close() error
}

// ParticipantSource injects a participant whose media originates OUTSIDE WebRTC.
// The IVR prompt player and a channel's phone/PSTN leg both implement it: they
// appear in the room as ordinary participants publishing Opus. pion never sees a
// channel-native codec — a Plug transcodes (e.g. G.711↔Opus) before the source,
// so ReadRTP always yields Opus.
type ParticipantSource interface {
	ID() ParticipantID
	ReadRTP() (*rtp.Packet, error)
	Close() error
}

// Supervisor is a participant that subscribes to a room's tracks WITHOUT
// publishing — the basis for barge/whisper (Phase 4).
type Supervisor interface {
	Subscribe(room RoomID) error
}

// InboundCall is the notification a Plug raises for an incoming PSTN call
// before it becomes a room participant.
type InboundCall struct {
	CallID CallID
	From   string // caller line identity (E.164)
	To     string // dialed number (the DID — resolves the tenant + IVR flow)
}

// Leg is one ANSWERED call's media + events, ready to plug into a room
// (sfu.AttachPort): the caller's audio enters as Source, the room's audio goes
// back via Write, keypresses arrive on DTMF, and Done closes when the far side
// hangs up. Source always yields Opus — the plug owns any transcode, so the
// media plane never sees a channel-native codec.
type Leg struct {
	Source ParticipantSource
	Write  func(pkt *rtp.Packet) error
	DTMF   <-chan string
	Done   <-chan struct{}
}

// Plug is the control contract for a voice channel — THE PLUG. FreeSWITCH is the
// first adapter, never a dependency: all channel-specific code lives in one
// adapter package that nothing in internal/media imports. Web and WhatsApp
// implement the same contract. Adding a channel = writing a sibling adapter,
// never touching the core.
type Plug interface {
	// OnInbound registers the single consumer for incoming calls. The consumer
	// must Answer or Reject promptly (the plug times the call out otherwise).
	OnInbound(func(InboundCall))
	Answer(id CallID) (*Leg, error)
	Reject(id CallID) error
	Hangup(id CallID) error
}

// OriginateRequest describes an outbound call the media plane asks a channel to
// place. Trunks is an ordered preference list (the plug/edge tries them in
// order or hands the whole set to the edge for failover).
type OriginateRequest struct {
	To       string   // E.164 destination
	CallerID string   // presented caller line (an allocated DID)
	Trunks   []string // ordered provider/gateway names
}

// Progress is an outbound call's pre-answer state, reported on the Originate
// callback so the caller (an agent) hears ringback.
type Progress struct {
	State string // "ringing" | "early_media"
}

// Originator is the outbound half of a voice channel — optional; a channel may
// implement Plug (inbound), Originator (outbound), or both. The FreeSWITCH plug
// implements both. Originate blocks until the call is answered (returns a Leg),
// fails, or ctx is cancelled; ring/early-media progress arrives on onProgress.
type Originator interface {
	Originate(ctx context.Context, req OriginateRequest, onProgress func(Progress)) (*Leg, error)
}
