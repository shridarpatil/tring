package room_test

import (
	"github.com/pion/rtp"

	"github.com/shridarpatil/tring/internal/media/room"
)

// genericPlug is a channel-agnostic plug (no phone/SIP concept) — its mere
// existence asserts room.Plug/room.Leg are transport-neutral, the whole point of
// Phase 1. If someone re-introduces a PSTN-only method on room.Plug, this stops
// compiling.
type genericPlug struct{}

func (genericPlug) OnInbound(func(room.InboundCall))      {}
func (genericPlug) Answer(room.CallID) (*room.Leg, error) { return &room.Leg{}, nil }
func (genericPlug) Reject(room.CallID) error              { return nil }
func (genericPlug) Hangup(room.CallID) error              { return nil }

type genericSource struct{}

func (genericSource) ID() room.ParticipantID    { return "src" }
func (genericSource) ReadRTP() (*rtp.Packet, error) { return &rtp.Packet{}, nil }
func (genericSource) Close() error               { return nil }

// Compile-time guarantees the contract is implementable without any transport.
var (
	_ room.Plug             = genericPlug{}
	_ room.ParticipantSource = genericSource{}
)
