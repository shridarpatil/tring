// Public types for the Tring browser SDK.

export interface InitOpts {
  /**
   * Short-lived JWT issued by `GET /v1/agents/{id}/token` (5-min TTL).
   * The integrating CRM's backend mints this with its API key and ships it
   * to the browser. The browser MUST NOT ever have the raw API key.
   */
  token: string;

  /**
   * Tring API base URL, e.g. "https://api.tring.io" or
   * "http://localhost:8085" for self-host dev.
   */
  apiBase: string;

  /**
   * When the SDK receives an inbound INVITE (the A-leg of an originated
   * call, or an inbound customer call to a masked DID), should it accept
   * automatically? Default: true. Set false to gate on a UI confirm.
   */
  autoAnswer?: boolean;
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
}

export type CallStatus = "ringing" | "connected" | "ended";
export type CallDirection = "inbound" | "outbound";

export type TringEvent =
  | { type: "registered" }
  | { type: "unregistered"; reason?: string }
  | { type: "call:ringing"; call: Call }
  | { type: "call:answered"; call: Call }
  | { type: "call:ended"; call: Call; cause?: string }
  | { type: "error"; error: Error };

// ---- Guest (web-to-web customer) calling --------------------------------

export interface GuestInitOpts {
  /**
   * Tring media gateway base URL, e.g. "https://gw.tring.io" (ADR-001). When
   * set, the visitor peers directly with the gateway over raw WebRTC and never
   * touches SIP/WSS or FreeSWITCH — the preferred web-calling path. Combine
   * with `flowId` to pin the call to an IVR flow. When omitted, the SDK falls
   * back to the legacy SIP-over-WSS path via minted credentials.
   */
  gatewayUrl?: string;
  /**
   * ICE servers for the gateway WebRTC path. Optional — defaults to a public
   * STUN server. Provide TURN entries for visitors behind symmetric NAT.
   */
  iceServers?: RTCIceServer[];
  /**
   * Optional agent/guest token, sent as a bearer to the gateway when it
   * requires auth. Anonymous visitor calls leave this unset.
   */
  token?: string;
  /**
   * Tenant public widget key (`pk_live_…`). Safe to embed in a public web
   * page — it only authorizes minting a flow-pinned guest identity, never
   * tenant access. Issued via GET /v1/guest-config. Required for the
   * anonymous-visitor flow; omit when using credentialsUrl/fetchCredentials.
   */
  publicKey?: string;
  /** Tring API base, e.g. "https://api.tring.io". Used for the public mint. */
  apiBase?: string;
  /**
   * Optional flow UUID to route this guest into. Omit to use the tenant's
   * configured support flow (the common case).
   */
  flowId?: string;
  /**
   * Bring-your-own-credentials (the trusted, logged-in-caller flow). A URL on
   * YOUR backend that returns a GuestCredentialsResponse — the creds your
   * backend obtained from Tring's authenticated mint
   * (`POST /v1/guest-credentials` with the tenant API key + a verified
   * `client_ref`). Fetched with `credentials: "include"` so your session
   * cookie authenticates the caller. When set, the public mint
   * (publicKey/apiBase) is bypassed, so the caller identity can't be spoofed
   * from the browser.
   */
  credentialsUrl?: string;
  /**
   * Programmatic alternative to credentialsUrl: return the credentials
   * yourself (fetch them however you like). Takes precedence over
   * credentialsUrl and the public mint.
   */
  fetchCredentials?: () => Promise<GuestCredentialsResponse>;
}

/** /v1/sdk/guest-credentials response: SIP creds + the IVR dial target. */
export interface GuestCredentialsResponse extends SipCredentialsResponse {
  dial_target: string;
}

export type GuestCallStatus =
  | "idle"
  | "connecting"
  | "ringing"
  | "connected"
  | "ended";

export type GuestEvent =
  | { type: "connecting" } // fetching creds / registering
  | { type: "ringing" } // INVITE sent into the IVR, awaiting answer
  | { type: "connected" } // IVR answered, media flowing
  | { type: "ended"; reason?: string }
  | { type: "error"; error: Error };

export interface SipCredentialsResponse {
  sip_username: string;
  sip_password: string;
  sip_domain: string;
  ws_url: string;
  /**
   * ICE servers for WebRTC media setup. Always includes at least one
   * STUN entry; TURN entries (carry username + credential) appear when
   * the Tring server has TRING_TURN_URL configured. Browsers behind
   * symmetric NAT or CGNAT need TURN to relay media — pure STUN fails.
   * Passed through to RTCPeerConnection unchanged.
   */
  ice_servers: RTCIceServer[];
}
