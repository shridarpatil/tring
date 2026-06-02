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
  SipCredentialsResponse,
  GuestInitOpts,
  GuestEvent,
  GuestCallStatus,
  GuestCredentialsResponse,
} from "./types";

// Default export so the IIFE bundle exposes `window.Tring` directly.
import { Tring } from "./tring";
export default Tring;
