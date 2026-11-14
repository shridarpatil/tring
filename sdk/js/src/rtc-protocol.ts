// FROZEN /rtc wire contract — the public client⇄edge protocol.
//
// This mirrors internal/media/signaling/protocol.go field-for-field. It is the
// STABLE contract between any browser client and the media edge: the Phase-2
// brain/edge split (moving the IVR/routing into the orchestrator) is a
// server-internal change and MUST NOT alter these messages. Freeze it. See
// docs/src/content/docs/architecture/overview.md.
//
// The SFU is the sole offerer, so the directions are:
//   client → server:  join | register | answer | ice | publish | unpublish | dtmf | bye
//                     | hold | resume | transfer | decline | ping
//                     (hold/resume/transfer are agent-only: a visitor token gets
//                     `error` / `transfer-failed` and the message is ignored)
//   server → client:  joined | offer | ice | peer-joined | peer-state | peer-left
//                     | incoming | incoming-cancel | ready | availability | error
//                     | supervisor | transfer-failed | resume-call | superseded
// Trickle ICE rides `ice` both ways. Resumption: the client stores
// `session_token` from `joined` and re-sends it in a later `join`.

export const ClientMsgType = {
  Join: "join",
  Register: "register",
  Answer: "answer",
  ICE: "ice",
  Publish: "publish",
  Unpublish: "unpublish",
  DTMF: "dtmf",
  Hold: "hold",
  Resume: "resume",
  Transfer: "transfer",
  Decline: "decline",
  Ping: "ping",
  Bye: "bye",
} as const;

export const ServerMsgType = {
  Joined: "joined",
  Offer: "offer",
  ICE: "ice",
  PeerJoined: "peer-joined",
  PeerState: "peer-state",
  PeerLeft: "peer-left",
  Incoming: "incoming",
  IncomingCancel: "incoming-cancel",
  Ready: "ready",
  Availability: "availability",
  Ended: "ended",
  Error: "error",
  Supervisor: "supervisor",
  // agent: your transfer is ringing the target; you stay on the call (cut off
  // from it) until they answer (then `ended`) or don't (`transfer-failed`).
  TransferOK: "transfer-ok",
  TransferFailed: "transfer-failed",
  Answered: "answered",
  // agent presence: a page refresh dropped this agent's call, and the gateway is
  // holding their slot for the grace window — `join` {room} to resume it.
  ResumeCall: "resume-call",
  // agent presence: another tab/device registered as this agent and now gets
  // the rings. Don't reconnect (two tabs would steal it back and forth forever).
  Superseded: "superseded",
} as const;

/** Browser-facing STUN/TURN entry sent in `joined` (shape matches RTCIceServer). */
export interface RtcIceServer {
  urls: string[];
  username?: string;
  credential?: string;
}

/**
 * The single JSON envelope for every /rtc message (matches the Go `msg` struct;
 * unused fields are omitted). One flat shape carries both directions.
 */
export interface RtcMessage {
  type: string;

  // join
  room?: string;
  name?: string;
  session_token?: string;
  flow_id?: string;

  // dtmf
  digit?: string;

  // offer / answer
  sdp?: string;

  // publish / unpublish
  kind?: "audio" | "video";

  // ice — an RTCIceCandidateInit, passed through opaquely
  candidate?: RTCIceCandidateInit;

  // joined / peer-* events
  participant_id?: string;
  state?: string;
  ice_servers?: RtcIceServer[];

  // incoming (server → agent); auto = the agent's own click-to-call, answer it
  // without asking
  from?: string;
  auto?: boolean;
  // incoming / resume-call: the other party is on a phone (carrier) leg, not a
  // browser — it can't receive a screen share
  phone?: boolean;

  // transfer (agent → server): re-route target
  to_team?: string;
  to_sip?: string;

  // error
  reason?: string;

  // supervisor (server → agent legs only)
  present?: boolean;
  mode?: string;

  // availability (server → agent presence socket): the backend Away/Break toggle
  available?: boolean;
}
