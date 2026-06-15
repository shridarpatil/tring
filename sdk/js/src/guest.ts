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
import { RtcCall } from "./rtc";
import type {
  GuestCredentialsResponse,
  GuestEvent,
  GuestInitOpts,
} from "./types";

export class TringGuest extends Emitter<GuestEvent> {
  private rtc: RtcCall | null = null;
  private muted = false;

  constructor(private readonly opts: GuestInitOpts) {
    super();
  }

  get isMuted(): boolean {
    return this.muted;
  }

  get inCall(): boolean {
    return this.rtc !== null;
  }

  get isSharingScreen(): boolean {
    return this.rtc?.sharingScreen ?? false;
  }

  /**
   * Acquire the mic, connect to the media plane over /rtc, and place the call
   * into the IVR. Emits connecting → ringing → connected → ended. Throws
   * (and emits `error`) on credential resolution or media failure.
   */
  async call(): Promise<void> {
    if (this.inCall) return; // already in a call — ignore double taps
    this.emit({ type: "connecting" });

    // Capture the mic FIRST — synchronously in the click/tap that started this
    // call, BEFORE the credentials fetch below. iOS Safari only grants
    // getUserMedia while the gesture is active, so capturing after an await
    // throws. Needs HTTPS (mobile Safari has no mediaDevices on http://).
    if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) {
      const e = new Error(
        "Microphone needs HTTPS — load this page over https:// (it won't work over http on mobile).",
      );
      this.emit({ type: "error", error: e });
      throw e;
    }
    let mic: MediaStream;
    try {
      mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      throw e;
    }

    // Resolve connection details: BYOC (trusted) overrides config.
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
      mic.getTracks().forEach((t) => t.stop());
      this.emit({ type: "error", error: e as Error });
      throw e;
    }

    if (!gatewayUrl) {
      mic.getTracks().forEach((t) => t.stop());
      const e = new Error(
        "TringGuest: gatewayUrl (or credentialsUrl/fetchCredentials) is required",
      );
      this.emit({ type: "error", error: e });
      throw e;
    }

    // One room per call; the edge namespaces it by tenant (from the token). The
    // visitor's flow is pinned server-side by the token — flowId here is only a
    // hint for the dev/agent-token path.
    const room = "v-" + randomId();
    const call = new RtcCall({
      gatewayUrl,
      room,
      token: token ?? "",
      flowId,
      iceServers,
      mic,
    });
    this.rtc = call;
    this.muted = false;
    call.on("connected", () => this.emit({ type: "connected" }));
    call.on("ended", (e) => {
      this.emit({ type: "ended", reason: e.reason });
      void this.cleanup();
    });
    call.on("error", (e) => this.emit({ type: "error", error: e.error }));
    // The agent's screen-share arrives as a video track; surface it to the UI,
    // and clear it when that track ends (the agent stopped sharing).
    call.on("track", (e) => {
      if (e.kind !== "video") return;
      this.emit({ type: "remote-video", stream: e.stream });
      e.track.onended = () => this.emit({ type: "remote-video", stream: null });
    });

    try {
      await call.join();
      this.emit({ type: "ringing" }); // placed into the IVR, awaiting answer
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      await this.cleanup();
      throw e;
    }
  }

  /** Hang up (or cancel a not-yet-answered) call. Safe to call any time. */
  async hangup(): Promise<void> {
    if (!this.rtc) return;
    this.rtc.hangup();
    this.emit({ type: "ended" });
    await this.cleanup();
  }

  /** Send a DTMF digit (0-9, *, #) to the IVR mid-call. */
  async sendDTMF(digit: string): Promise<void> {
    this.rtc?.sendDtmf(digit);
  }

  /** Start sharing the visitor's screen with the agent. Prompts the OS
   *  screen-picker — call from a user gesture (e.g. a button click). */
  async shareScreen(constraints?: DisplayMediaStreamOptions): Promise<void> {
    if (!this.rtc) throw new Error("not in a call");
    await this.rtc.shareScreen(constraints);
  }

  /** Stop sharing the visitor's screen. */
  stopScreen(): void {
    this.rtc?.stopScreen();
  }

  /** Flip screen-share on/off; returns the resulting state. */
  async toggleScreen(): Promise<boolean> {
    if (this.isSharingScreen) {
      this.stopScreen();
      return false;
    }
    await this.shareScreen();
    return true;
  }

  /** Mute/unmute the visitor's microphone. */
  async mute(muted: boolean): Promise<void> {
    if (!this.rtc) return;
    this.rtc.setMuted(muted);
    this.muted = muted;
  }

  /** Convenience: flip mute and return the new state. */
  toggleMute(): boolean {
    const next = !this.muted;
    void this.mute(next);
    return next;
  }

  private async cleanup(): Promise<void> {
    const call = this.rtc;
    this.rtc = null;
    this.muted = false;
    call?.hangup();
  }
}

// randomId is a per-call room id (crypto.randomUUID where available).
function randomId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
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
