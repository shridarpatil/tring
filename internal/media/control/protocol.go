// Package control is the PRIVATE brain↔edge control channel for the media plane
// (Phase 2 of docs/ARCHITECTURE.md).
//
// The orchestrator (BRAIN) runs the IVR + routing for web/WhatsApp calls; the
// gateway (EDGE) only moves media. They talk over one authenticated WebSocket:
// EVENTS flow up (call lifecycle, DTMF, agent presence) and COMMANDS flow down
// (play a prompt, ring an agent, start/stop recording, drop a participant). This
// keeps business logic OFF the public edge — the edge holds no DB, no flow logic,
// no routing. The link is server-to-server on a private network, gated by a
// shared secret.
//
// 2a builds the transport + this frozen envelope; 2b/2c move the IVR walker and
// the ring onto it. The envelope is one flat JSON shape for both directions.
package control

// Message is the single JSON envelope for every control-channel message.
type Message struct {
	Type string `json:"type"`
	// ID correlates a command with its ack (optional; empty = fire-and-forget).
	ID string `json:"id,omitempty"`

	// addressing — every call event/command carries the room (tenant-namespaced
	// "<tenant>/<name>") so the brain can key per-call state and the edge can
	// target the right room.
	Tenant      string `json:"tenant,omitempty"`
	Room        string `json:"room,omitempty"`
	Participant string `json:"participant_id,omitempty"`

	// event payloads (edge → brain)
	FlowID    string `json:"flow_id,omitempty"`
	ClientRef string `json:"client_ref,omitempty"`
	Name      string `json:"name,omitempty"`
	Digit     string `json:"digit,omitempty"`
	Sip       string `json:"sip,omitempty"`

	// command payloads (brain → edge)
	File string `json:"file,omitempty"` // prompt audio to play / inject
	Role string `json:"role,omitempty"` // recording leg role (agent/customer)
	From string `json:"from,omitempty"` // ring label shown to the agent
	Team string `json:"team,omitempty"` // ring target: a team (else Sip, else ring-all)

	// response (edge → brain, echoing ID)
	OK      bool   `json:"ok,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Outcome string `json:"outcome,omitempty"` // ring/node result, e.g. "completed" / "no_answer"
}

// Event types — EDGE → BRAIN.
const (
	EvCallStarted       = "call.started"       // {tenant, room, participant_id, flow_id, client_ref, name}
	EvCallEnded         = "call.ended"         // {tenant, room}
	EvDTMF              = "dtmf"               // {room, participant_id, digit}
	EvParticipantJoined = "participant.joined" // {room, participant_id, name}
	EvParticipantLeft   = "participant.left"   // {room, participant_id}
	EvAgentOnline       = "agent.online"       // {tenant, sip}
	EvAgentOffline      = "agent.offline"      // {tenant, sip}
	// agent call control — the agent acts on their own active call
	EvHold     = "agent.hold"     // {room, participant_id}
	EvResume   = "agent.resume"   // {room, participant_id}
	EvTransfer = "agent.transfer" // {room, participant_id (requester), team | sip}
)

// Command types — BRAIN → EDGE.
const (
	CmdPlay        = "play"               // {room, file} — inject a prompt into the room
	CmdIVRStop     = "ivr.stop"           // {room} — remove the IVR voice participant
	CmdRing        = "ring"               // {tenant, room, sip?, from} — push `incoming` to agent(s); empty sip = ring all online
	CmdRecordStart = "record.start"       // {room} — begin recording the room's legs
	CmdRecordStop  = "record.stop"        // {room}
	CmdRemove      = "participant.remove" // {room, participant_id} — drop a leg (e.g. transferred-out agent)
	CmdHold        = "hold"               // {room} — inject hold music toward the other party
	CmdResume      = "resume"             // {room} — stop hold music
)
