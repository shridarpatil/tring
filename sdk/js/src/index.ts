// Public SDK surface. Re-exports the Tring class and the typed shapes the
// integrating app uses.

export { Tring } from "./tring";
export type {
  Call,
  CallStatus,
  InitOpts,
  TringEvent,
  SipCredentialsResponse,
} from "./types";

// Default export so the IIFE bundle exposes `window.Tring` directly.
import { Tring } from "./tring";
export default Tring;
