// Public SDK surface. Re-exports the Tring class and the typed shapes the
// integrating app uses.

export { Tring } from "./tring";
export { TringGuest } from "./guest";
export type {
  Call,
  CallStatus,
  CallDirection,
  InitOpts,
  TringEvent,
  GuestInitOpts,
  GuestEvent,
  GuestCallStatus,
  GuestCredentialsResponse,
} from "./types";

// /rtc media-plane client (pion). Headless calling primitives speaking the
// FROZEN /rtc contract — the SDK home for the logic in examples/rtc-*.html.
export { RtcCall, RtcAgent } from "./rtc";
export type {
  RtcCallOpts,
  RtcCallEvent,
  RtcAgentOpts,
  RtcAgentEvent,
} from "./rtc";
export type { RtcMessage, RtcIceServer } from "./rtc-protocol";
export { ClientMsgType, ServerMsgType } from "./rtc-protocol";

// Default export so the IIFE bundle exposes `window.Tring` directly.
import { Tring } from "./tring";
export default Tring;
