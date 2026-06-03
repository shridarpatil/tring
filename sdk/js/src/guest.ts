// TringGuest — the customer (web-to-web) side of calling.
//
// A website visitor with no account places a WebRTC call into a tenant's
// IVR flow. Unlike the agent SDK (Tring), there is no JWT and no API-driven
// originate: the guest fetches an ephemeral, flow-pinned SIP identity with
// the tenant's *public* key, registers, and sends the INVITE itself. The
// backend dialplan pins the call to the IVR (a guest can never reach a
// trunk), so the dial target is fixed and supplied by the credentials
// response.
//
// Lazy by design: nothing connects until the visitor actually calls, so a
// page that merely *embeds* the widget opens zero WebSocket connections.

import { SessionState, type Inviter } from "sip.js";

import { ApiClient } from "./api";
import { Emitter } from "./events";
import { SipAdapter } from "./sip";
import type {
  GuestCredentialsResponse,
  GuestEvent,
  GuestInitOpts,
} from "./types";

export class TringGuest extends Emitter<GuestEvent> {
  private readonly api: ApiClient;
  private sip: SipAdapter | null = null;
  private session: Inviter | null = null;
  private muted = false;

  constructor(private readonly opts: GuestInitOpts) {
    super();
    this.api = new ApiClient(opts.apiBase ?? "");
  }

  get isMuted(): boolean {
    return this.muted;
  }

  get inCall(): boolean {
    return this.session !== null;
  }

  /**
   * Fetch guest credentials, register over WSS, and place the call into the
   * IVR. Emits connecting → ringing → connected → ended. Throws (and emits
   * `error`) if credential mint or media setup fails.
   */
  async call(): Promise<void> {
    if (this.session) return; // already in a call — ignore double taps
    this.emit({ type: "connecting" });

    let creds: GuestCredentialsResponse;
    try {
      if (this.opts.fetchCredentials) {
        // Programmatic bring-your-own-credentials.
        creds = await this.opts.fetchCredentials();
      } else if (this.opts.credentialsUrl) {
        // Trusted path: creds come from the integrator's own backend.
        creds = await this.api.fetchCredentialsFromURL(this.opts.credentialsUrl);
      } else {
        // Anonymous-visitor path: Tring's public mint (no caller identity).
        creds = await this.api.fetchGuestCredentials(
          this.opts.publicKey ?? "",
          this.opts.flowId,
        );
      }
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      throw e;
    }

    this.sip = new SipAdapter({
      onInvitation: () => {}, // guests place calls, never receive them
      onRegistered: () => {},
      onUnregistered: () => {},
    });

    try {
      await this.sip.connect(creds);
      this.muted = false;
      const session = await this.sip.invite(
        `sip:${creds.dial_target}@${creds.sip_domain}`,
      );
      this.session = session;
      this.emit({ type: "ringing" });

      session.stateChange.addListener((state) => {
        if (state === SessionState.Established) {
          this.emit({ type: "connected" });
        } else if (state === SessionState.Terminated) {
          this.emit({ type: "ended" });
          void this.cleanup();
        }
      });
    } catch (e) {
      this.emit({ type: "error", error: e as Error });
      await this.cleanup();
      throw e;
    }
  }

  /** Hang up (or cancel a not-yet-answered) call. Safe to call any time. */
  async hangup(): Promise<void> {
    const s = this.session;
    if (!s) return;
    try {
      switch (s.state) {
        case SessionState.Established:
          await s.bye();
          break;
        case SessionState.Initial:
        case SessionState.Establishing:
          await s.cancel();
          break;
      }
    } catch {
      /* terminating an already-dead session is fine */
    }
  }

  /**
   * Send a DTMF digit (0-9, *, #) to the IVR mid-call. Prefers RFC 2833
   * in-band tones (what WebRTC + FreeSWITCH `play_and_get_digits` expect);
   * falls back to SIP INFO if the session description handler can't.
   */
  async sendDTMF(digit: string): Promise<void> {
    const s = this.session;
    if (!s) return;
    const sdh = s.sessionDescriptionHandler as
      | { sendDtmf?: (tone: string) => boolean }
      | undefined;
    if (sdh?.sendDtmf?.(digit)) return; // RFC 2833 succeeded
    try {
      await s.info({
        requestOptions: {
          body: {
            contentDisposition: "render",
            contentType: "application/dtmf-relay",
            content: `Signal=${digit}\r\nDuration=160`,
          },
        },
      });
    } catch {
      /* best-effort — a dead session swallows the digit */
    }
  }

  /** Mute/unmute the visitor's microphone. */
  async mute(muted: boolean): Promise<void> {
    if (this.sip && this.session) {
      this.sip.setMuted(this.session, muted);
      this.muted = muted;
    }
  }

  /** Convenience: flip mute and return the new state. */
  toggleMute(): boolean {
    const next = !this.muted;
    void this.mute(next);
    return next;
  }

  private async cleanup(): Promise<void> {
    this.session = null;
    this.muted = false;
    const sip = this.sip;
    this.sip = null;
    await sip?.disconnect().catch(() => {});
  }
}
