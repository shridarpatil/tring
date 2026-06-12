package signaling

import "encoding/json"

// Wire protocol for the /rtc WebSocket. One JSON envelope type carries every
// message; unused fields are omitted. The SFU is the sole offerer, so the
// directions are:
//
//	client → server:  join, answer, ice, bye
//	server → client:  joined, offer, ice, peer-joined, peer-state, peer-left, error
//
// Trickle ICE rides `ice` in both directions. Session resumption: the client
// stores session_token from `joined` and re-sends it in a later `join`.
const (
	// client → server
	msgJoin      = "join"      // {room, name, session_token?, flow_id?}
	msgRegister  = "register"  // agent presence: this socket is an available agent
	msgAnswer    = "answer"    // {sdp} — answer to a server offer
	msgICE       = "ice"       // {candidate}
	msgPublish   = "publish"   // {kind} — ask the server to invite an extra track (screen-share video)
	msgUnpublish = "unpublish" // {kind} — stop publishing that track
	msgDTMF      = "dtmf"      // {digit} — a caller keypress, routed to the room's IVR
	msgHold      = "hold"      // agent: put the other party on hold (they hear MOH)
	msgResume    = "resume"    // agent: take the other party off hold
	msgTransfer  = "transfer"  // agent: re-route the call — {to_team | to_sip}
	msgBye       = "bye"

	// server → client
	msgJoined     = "joined"      // {participant_id, session_token, ice_servers}
	msgOffer      = "offer"       // {sdp}
	msgPeerJoined = "peer-joined" // {participant_id, name}
	msgPeerState  = "peer-state"  // {participant_id, state}
	msgPeerLeft   = "peer-left"   // {participant_id}
	msgIncoming   = "incoming"    // s→agent: a call is ringing — {room, from}
	msgReady      = "ready"       // s→agent: presence registered
	msgError      = "error"       // {reason}
)

// msg is the single envelope for every /rtc message.
type msg struct {
	Type string `json:"type"`

	// join
	Room   string `json:"room,omitempty"`
	Name   string `json:"name,omitempty"`
	Token  string `json:"session_token,omitempty"`
	FlowID string `json:"flow_id,omitempty"` // pins an IVR flow to run in this room

	// dtmf
	Digit string `json:"digit,omitempty"`

	// offer / answer
	SDP string `json:"sdp,omitempty"`

	// publish / unpublish — "audio" | "video"
	Kind string `json:"kind,omitempty"`

	// ice — a webrtc.ICECandidateInit, passed through opaquely
	Candidate json.RawMessage `json:"candidate,omitempty"`

	// joined / peer-* events
	Participant string      `json:"participant_id,omitempty"`
	State       string      `json:"state,omitempty"`
	ICEServers  []ICEServer `json:"ice_servers,omitempty"`

	// incoming (s→agent): who's calling (the IVR transfer label)
	From string `json:"from,omitempty"`

	// transfer (agent → server): the re-route target — a team or one agent
	ToTeam string `json:"to_team,omitempty"`
	ToSip  string `json:"to_sip,omitempty"`

	// error / bye
	Reason string `json:"reason,omitempty"`
}

// ICEServer is the browser-facing STUN/TURN entry sent in `joined` (ephemeral
// TURN creds are minted per join from the shared secret).
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
