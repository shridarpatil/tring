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
  /** Destination as supplied to `call(to)`. */
  to: string;
  /** Current state. */
  status: CallStatus;
  /** Hang up this call. */
  hangup(): Promise<void>;
  /** Send a DTMF digit (0-9, *, #) mid-call. */
  sendDTMF(digit: string): Promise<void>;
}

export type CallStatus = "ringing" | "connected" | "ended";

export type TringEvent =
  | { type: "registered" }
  | { type: "unregistered"; reason?: string }
  | { type: "call:ringing"; call: Call }
  | { type: "call:answered"; call: Call }
  | { type: "call:ended"; call: Call; cause?: string }
  | { type: "error"; error: Error };

export interface SipCredentialsResponse {
  sip_username: string;
  sip_password: string;
  sip_domain: string;
  ws_url: string;
}
