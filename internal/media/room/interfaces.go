package room

import (
	"github.com/pion/rtp"
)

// The four extension interfaces are the seams that keep browser and non-browser
// participants indistinguishable to the room. They are DEFINED here in M1 and
// exercised by stubs/tests; their real implementations land in later phases
// (see docs/ARCHITECTURE.md). Keeping them in the core package — and free of
// any FreeSWITCH/SIP concept — is what lets the PSTN side be a swappable adapter.

// ParticipantID identifies a participant within its room. Stable across a
// transport drop + reattach, so the slot (and the peer's subscription to it)
// survives a refresh.
type ParticipantID string

// RoomID identifies a call/room.
type RoomID string

// CallID identifies a PSTN-side call leg (Plug scope), distinct from the
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
// The IVR prompt player (M8, the first real implementation) and the FreeSWITCH
// phone leg (Phase 2) both implement it: they appear in the room as ordinary
// participants publishing Opus. pion never sees PSTN codecs — a Plug
// transcodes G.711↔Opus before the source, so ReadRTP always yields Opus.
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
//
// Outbound origination grows the contract later (Originator, Phase 3) — inbound
// lands first.
type Plug interface {
	OnInbound(func(InboundCall))
	Answer(id CallID) (*Leg, error)
	Reject(id CallID) error
	Hangup(id CallID) error
}
