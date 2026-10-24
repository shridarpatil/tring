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
	msgJoin      = "join"           // {room, name, session_token?, flow_id?}
	msgRegister  = "register"       // agent presence: this socket is an available agent
	msgAnswer    = "answer"         // {sdp} — answer to a server offer
	msgICE       = "ice"            // {candidate}
	msgPublish   = "publish"        // {kind} — ask the server to invite an extra track (screen-share video)
	msgUnpublish = "unpublish"      // {kind} — stop publishing that track
	msgDTMF      = "dtmf"           // {digit} — a caller keypress, routed to the room's IVR
	msgHold      = "hold"           // agent: put the other party on hold (they hear MOH)
	msgResume    = "resume"         // agent: take the other party off hold
	msgTransfer  = "transfer"       // agent: re-route the call — {to_team | to_sip}
	msgDecline   = "decline"        // agent: reject a ring so the router advances to the next agent NOW — {room}
	msgSupervise = "supervise-mode" // supervisor → server: switch mode — {mode: monitor|whisper|barge}
	msgPing      = "ping"           // keepalive: a no-op frame that resets the socket read deadline
	msgBye       = "bye"
	msgDial      = "dial" // agent: originate an outbound PSTN call into this room — {to}

	// server → client
	msgJoined         = "joined"          // {participant_id, session_token, ice_servers}
	msgOffer          = "offer"           // {sdp}
	msgPeerJoined     = "peer-joined"     // {participant_id, name}
	msgPeerState      = "peer-state"      // {participant_id, state}
	msgPeerLeft       = "peer-left"       // {participant_id}
	msgIncoming       = "incoming"        // s→agent: a call is ringing — {room, from}
	msgCancelRing     = "incoming-cancel" // s→agent: withdraw a ring (advanced to next agent / someone answered) — {room}
	msgReady          = "ready"           // s→agent: presence registered
	msgAvailability   = "availability"    // s→agent: backend Away/Break toggle — {available}; sent after ready and on every change
	msgEnded          = "ended"           // s→client: the call is over (terminal — don't reconnect)
	msgError          = "error"           // {reason}
	msgTransferOK     = "transfer-ok"     // s→agent: blind transfer accepted; this leg will end
	msgTransferFailed = "transfer-failed" // s→agent: transfer rejected — {reason}; agent keeps the call
	msgSupervisor     = "supervisor"      // s→agent: a supervisor is present — {present, mode, name}
	msgRinging        = "ringing"         // s→agent: the outbound PSTN call is ringing (pre-answer progress)
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

	// dial (agent → server): the outbound destination
	To string `json:"to,omitempty"`

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

	// supervisor
	Mode    string `json:"mode,omitempty"`
	Present bool   `json:"present,omitempty"`

	// availability (s→agent): a pointer so `false` (Away) still goes on the wire
	Available *bool `json:"available,omitempty"`
}

// ICEServer is the browser-facing STUN/TURN entry sent in `joined` (ephemeral
// TURN creds are minted per join from the shared secret).
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
