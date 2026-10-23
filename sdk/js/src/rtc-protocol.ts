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
//                     | incoming | incoming-cancel | ready | error | supervisor
//                     | transfer-failed
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
  Ended: "ended",
  Error: "error",
  Supervisor: "supervisor",
  TransferFailed: "transfer-failed",
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

  // incoming (server → agent)
  from?: string;

  // transfer (agent → server): re-route target
  to_team?: string;
  to_sip?: string;

  // error
  reason?: string;

  // supervisor (server → agent legs only)
  present?: boolean;
  mode?: string;
}
