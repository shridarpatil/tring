import type { LocalVideoState } from "./video";

// Public types for the Tring browser SDK.

export interface InitOpts {
  /**
   * Short-lived JWT issued by `GET /v1/agents/{id}/token` (5-min TTL).
   * The integrating CRM's backend mints this with its API key and ships it
   * to the browser. The browser MUST NOT ever have the raw API key.
   *
   * A static token can't outlive its TTL across a reconnect — prefer
   * `getToken` so the SDK re-mints a fresh token on every (re)connect.
   */
  token?: string;

  /**
   * Mint a fresh agent JWT on demand. Called on every (re)connect, so the
   * agent's socket survives drops past the 5-min token TTL. Typically fetches
   * from YOUR backend (which holds the API key). Takes precedence over `token`.
   */
  getToken?: () => Promise<string>;

  /**
   * For a TRUE inbound call (an IVR transfer / inbound DID ringing this agent),
   * should the SDK auto-answer? Default: true; set false to gate on a UI
   * confirm. A backend click-to-call leg always auto-answers (the agent already
   * initiated) — the gateway signals that per-call, regardless of this flag.
   *
   * Note: the SDK has NO Tring API access. Outbound calls are placed by the
   * integrator's backend (POST /v1/calls); the agent's leg then rings here.
   */
  autoAnswer?: boolean;

  /**
   * Tring media gateway base URL, e.g. "https://gw.tring.io" (ADR-001 M4b).
   * The agent peers with the gateway over raw WebRTC (a WebSocket for
   * signaling + presence) — the agent never speaks SIP and FreeSWITCH stays
   * private. Required.
   */
  gatewayUrl?: string;
  /**
   * ICE servers for the gateway WebRTC path. Optional — defaults to a public
   * STUN server. Provide TURN entries for agents behind symmetric NAT.
   */
  iceServers?: RTCIceServer[];
}

export interface Call {
  /** Tring call id (matches `calls.id` in the backend). */
  id?: string;
  /** Destination as supplied to `call(to)`, or the caller for inbound. */
  to: string;
  /**
   * `outbound` = the agent placed this call (their own leg auto-answers);
   * `inbound` = a call was bridged TO the agent (e.g. an IVR transfer) and,
   * unless autoAnswer is on, waits for `accept()`.
   */
  direction: CallDirection;
  /** True when the other party is on a phone (carrier) line, not a browser —
   *  screen share can't reach them. */
  phone: boolean;
  /** Display name of the remote party, when known (e.g. "Web Caller"). */
  displayName?: string;
  /** Current state. */
  status: CallStatus;
  /** Answer an inbound call. No-op for an already-answered call. */
  accept(): Promise<void>;
  /** Hang up (or, while still ringing, reject) this call. */
  hangup(): Promise<void>;
  /** Send a DTMF digit (0-9, *, #) mid-call. */
  sendDTMF(digit: string): Promise<void>;
  /** Mute or unmute the agent's microphone (local — stops sending audio). */
  setMuted(muted: boolean): void;
  /** Whether the agent's mic is currently muted. */
  readonly muted: boolean;
  /** Put the other party on hold music (the brain injects MOH). */
  hold(): void;
  /** Take the other party off hold. */
  resume(): void;
  /** Transfer the call to another agent or team. This agent stays on the call
   *  (muted, hearing nothing) until the target answers, then leaves; if nobody
   *  answers within the transfer timeout, the call comes back to them. */
  transfer(target: { toSip?: string; toTeam?: string }): void;
  /** Turn on the camera for a web call. Must be called from a user gesture. */
  startCamera(constraints?: MediaTrackConstraints): Promise<void>;
  stopCamera(): void;
  readonly cameraEnabled: boolean;
  readonly localVideoStream: MediaStream | null;
  /** Start publishing the agent's screen to the other party. Prompts the OS
   *  screen-picker (must be called from a user gesture). */
  shareScreen(constraints?: DisplayMediaStreamOptions): Promise<void>;
  /** Stop the agent's screen share. */
  stopScreen(): void;
  /** Whether the agent is currently sharing their screen. */
  readonly sharingScreen: boolean;
}

export type CallStatus = "ringing" | "connected" | "ended";
export type CallDirection = "inbound" | "outbound";

export type TringEvent =
  | { type: "registered" }
  // The backend marked this agent Away/Break (available: false) or back
  // (true) via POST /v1/agents/{id}/availability. Sent live on every change.
  | { type: "availability"; available: boolean }
  | { type: "unregistered"; reason?: string }
  | { type: "reconnecting"; attempt: number }
  // Another tab/device signed in as this agent and now gets the rings. This
  // one stays connected to no one (no auto-reconnect) until takeOver().
  | { type: "superseded" }
  | { type: "call:ringing"; call: Call }
  // The page reloaded mid-call and the SDK is rejoining that call on its own
  // (no accept needed); call:answered follows once it's back, else call:ended.
  | { type: "call:resuming"; call: Call }
  | { type: "call:answered"; call: Call }
  | { type: "call:ended"; call: Call; cause?: string }
  | ({ type: "call:local-video"; call: Call } & LocalVideoState)
  // The remote party's camera or screen track arrived (stream) or ended (null).
  | { type: "call:remote-video"; call: Call; stream: MediaStream | null }
  // A supervisor (console operator) is monitoring/whispering/barging on this
  // call, or has stopped. The customer is never notified — this is agent-only.
  | { type: "call:supervisor"; call: Call; present: boolean; mode: string; name: string }
  // This agent's transfer is ringing the target; they stay on the call (muted,
  // hearing nothing) until it's answered (call:ended) or not (below).
  | { type: "call:transferring"; call: Call }
  // The transfer didn't happen and the call is still this agent's: refused up
  // front (target unavailable, …) or nobody answered (reason "no answer").
  | { type: "call:transfer-failed"; call: Call; reason: string }
  | { type: "error"; error: Error };

// ---- Guest (web-to-web customer) calling --------------------------------

export interface GuestInitOpts {
  /**
   * Tring media gateway base URL, e.g. "https://gw.tring.io" (ADR-001). The
   * visitor peers directly with the gateway over raw WebRTC — FreeSWITCH stays
   * private. Required (anonymous path) unless credentialsUrl/fetchCredentials
   * supplies it.
   */
  gatewayUrl?: string;
  /**
   * ICE servers for the gateway WebRTC path. Optional — defaults to a public
   * STUN server. Provide TURN entries for visitors behind symmetric NAT.
   */
  iceServers?: RTCIceServer[];
  /**
   * Optional token, sent as a bearer to the gateway when it requires auth.
   * Anonymous visitor calls leave this unset.
   */
  token?: string;
  /**
   * Flow UUID to route this visitor into (the IVR flow the gateway pins the
   * call to). Required for the anonymous path; or supplied by
   * credentialsUrl/fetchCredentials.
   */
  flowId?: string;
  /**
   * Bring-your-own-credentials (the trusted, logged-in-caller flow). A URL on
   * YOUR backend that returns a GuestCredentialsResponse (gateway URL + flow id
   * + optional token/ICE) — e.g. derived from the verified logged-in caller.
   * Fetched with `credentials: "include"` so your session cookie authenticates
   * the request. When set, it supplies the gateway connection details.
   */
  credentialsUrl?: string;
  /**
   * Programmatic alternative to credentialsUrl: return the credentials
   * yourself. Takes precedence over credentialsUrl.
   */
  fetchCredentials?: () => Promise<GuestCredentialsResponse>;
}

/**
 * Gateway connection details for a visitor call (BYOC/trusted path). The
 * integrator's backend returns this so the browser never needs a tenant key.
 */
export interface GuestCredentialsResponse {
  gateway_url: string;
  flow_id: string;
  token?: string;
  ice_servers?: RTCIceServer[];
}

export type GuestCallStatus =
  | "idle"
  | "connecting"
  | "ringing"
  | "connected"
  | "ended";

export type GuestEvent =
  | { type: "connecting" } // acquiring mic / negotiating
  | { type: "ringing" } // offer sent to the gateway, awaiting answer
  | { type: "connected" } // IVR answered, media flowing
  | { type: "answered" } // agent accepted; conversation timer starts here
  | { type: "ended"; reason?: string }
  | ({ type: "local-video" } & LocalVideoState)
  // The agent's camera or screen track arrived (stream) or ended (null).
  | { type: "remote-video"; stream: MediaStream | null }
  | { type: "error"; error: Error };
