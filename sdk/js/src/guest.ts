// TringGuest — the visitor (web-to-web) side of calling, over the media gateway.
//
// A website visitor with no account places a WebRTC call into a tenant's IVR
// flow. The browser peers directly with the Tring media gateway (ADR-001) —
// raw WebRTC, HTTP offer/answer signaling — and the gateway bridges into a
// PRIVATE FreeSWITCH. No SIP, no WebSocket-to-FS, no mint.
//
// Connection details (gateway URL + flow id, optional token/ICE) come either
// from config (the anonymous "call us" path) or from the integrator's own
// backend via credentialsUrl/fetchCredentials (the trusted logged-in caller).
//
// Lazy by design: nothing connects until the visitor actually calls.

import { Emitter } from "./events";
import { GatewayAdapter } from "./gateway";
import type {
  GuestCredentialsResponse,
  GuestEvent,
  GuestInitOpts,
} from "./types";

export class TringGuest extends Emitter<GuestEvent> {
  private gw: GatewayAdapter | null = null;
  private muted = false;

  constructor(private readonly opts: GuestInitOpts) {
    super();
  }

  get isMuted(): boolean {
    return this.muted;
  }

  get inCall(): boolean {
    return this.gw !== null;
  }

  /**
   * Acquire the mic, negotiate WebRTC with the gateway, and place the call
   * into the IVR. Emits connecting → ringing → connected → ended. Throws
   * (and emits `error`) on credential resolution or media failure.
   */
  async call(): Promise<void> {
    if (this.inCall) return; // already in a call — ignore double taps
    this.emit({ type: "connecting" });

    // Capture the mic FIRST — synchronously in the click/tap that started this
    // call, BEFORE the credentials fetch below. iOS Safari (and some Android
    // cases) only grant getUserMedia while the gesture is active, so capturing
    // it after an await throws a permission error.
    const gw = new GatewayAdapter();
    this.gw = gw;
    this.muted = false;
    try {
      await gw.captureMic();
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      await this.cleanup();
      throw e;
    }

    // Resolve gateway connection details: BYOC (trusted) overrides config.
    let gatewayUrl = this.opts.gatewayUrl;
    let flowId = this.opts.flowId;
    let token = this.opts.token;
    let iceServers = this.opts.iceServers;
    try {
      let creds: GuestCredentialsResponse | undefined;
      if (this.opts.fetchCredentials) {
        creds = await this.opts.fetchCredentials();
      } else if (this.opts.credentialsUrl) {
        creds = await fetchCreds(this.opts.credentialsUrl);
      }
      if (creds) {
        gatewayUrl = creds.gateway_url;
        flowId = creds.flow_id;
        token = creds.token ?? token;
        iceServers = creds.ice_servers ?? iceServers;
      }
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      throw e;
    }

    if (!gatewayUrl) {
      const e = new Error(
        "TringGuest: gatewayUrl (or credentialsUrl/fetchCredentials) is required",
      );
      this.emit({ type: "error", error: e });
      throw e;
    }

    try {
      await gw.start({
        gatewayUrl,
        flowId,
        token,
        iceServers,
        onState: (state) => {
          if (state === "ringing") this.emit({ type: "ringing" });
          else if (state === "connected") this.emit({ type: "connected" });
          else if (state === "ended") {
            this.emit({ type: "ended" });
            void this.cleanup();
          }
        },
      });
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      await this.cleanup();
      throw e;
    }
  }

  /** Hang up (or cancel a not-yet-answered) call. Safe to call any time. */
  async hangup(): Promise<void> {
    if (!this.gw) return;
    await this.gw.stop();
    this.emit({ type: "ended" });
    await this.cleanup();
  }

  /** Send a DTMF digit (0-9, *, #) to the IVR mid-call (RFC 4733). */
  async sendDTMF(digit: string): Promise<void> {
    await this.gw?.sendDTMF(digit);
  }

  /** Mute/unmute the visitor's microphone. */
  async mute(muted: boolean): Promise<void> {
    if (!this.gw) return;
    this.gw.setMuted(muted);
    this.muted = muted;
  }

  /** Convenience: flip mute and return the new state. */
  toggleMute(): boolean {
    const next = !this.muted;
    void this.mute(next);
    return next;
  }

  private async cleanup(): Promise<void> {
    const gw = this.gw;
    this.gw = null;
    this.muted = false;
    await gw?.stop().catch(() => {});
  }
}

// fetchCreds pulls gateway connection details from the integrator's backend
// (sent with the session cookie so the logged-in caller is authenticated).
async function fetchCreds(url: string): Promise<GuestCredentialsResponse> {
  const r = await fetch(url, { credentials: "include" });
  if (!r.ok) {
    let msg = `credentials request failed (${r.status})`;
    try {
      const j = await r.json();
      if (j && typeof j.error === "string") msg = j.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(msg);
  }
  return r.json();
}
