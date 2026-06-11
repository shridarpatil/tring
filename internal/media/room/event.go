package room

// State is a participant's lifecycle stage. The transport (PeerConnection /
// signaling socket) drives the transitions, but the state lives on the room
// slot so it survives a transport drop:
//
//	joining ──connect──▶ connected ──drop──▶ reconnecting ──reattach+connect──▶ connected
//	   │                     │                    │
//	   └──leave/evict────────┴────────────────────┴──▶ left  (terminal)
//
// reconnecting is the grace window: the slot is held (and the peer kept) for a
// configurable duration so a hard browser refresh recovers instead of dropping
// the call. Grace expiry, an explicit bye, or a drop while still joining all
// end in left.
type State int

const (
	// StateJoining: the slot exists but media isn't up yet (initial negotiation).
	StateJoining State = iota
	// StateConnected: media is flowing.
	StateConnected
	// StateReconnecting: the transport dropped; the slot is held for the grace
	// window awaiting a reattach + fresh PeerConnection.
	StateReconnecting
	// StateLeft: terminal — the participant is gone (bye or grace expiry).
	StateLeft
)

func (s State) String() string {
	switch s {
	case StateJoining:
		return "joining"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateLeft:
		return "left"
	default:
		return "unknown"
	}
}

// EventType tags a room event for the signaling layer, which fans it out to the
// other participants' sockets (e.g. peer-joined / peer-state / peer-left).
type EventType string

const (
	// EventParticipantJoined: a new participant slot was created (Name set).
	EventParticipantJoined EventType = "participant_joined"
	// EventParticipantState: a participant changed lifecycle state (State set).
	EventParticipantState EventType = "participant_state"
	// EventParticipantLeft: a participant reached the terminal left state.
	EventParticipantLeft EventType = "participant_left"
)

// Event is one room occurrence. Fields not relevant to a Type are zero (e.g.
// State is only meaningful on EventParticipantState). The room emits events
// AFTER releasing its lock, so an emitter may safely call back into the room.
type Event struct {
	Type        EventType
	Room        RoomID
	Participant ParticipantID
	// Name is the participant's display label; set on EventParticipantJoined.
	Name string
	// State is the new state; set on EventParticipantState.
	State State
}

// Emitter receives room events. The signaling layer supplies one and routes
// each event to the relevant participant sockets. A nil Emitter is a valid
// no-op (events are dropped).
type Emitter func(Event)
