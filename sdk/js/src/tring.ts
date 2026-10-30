// Public entry point: the Tring class — the agent's media/signaling client.
//
// Usage:
//
//   const tring = await Tring.init({
//     token:      '<jwt from /v1/agents/{id}/token>',
//     gatewayUrl: 'http://localhost:8087',
//   });
//   tring.on('registered',    () => console.log('online'));
//   tring.on('call:ringing',  ({call}) => …);   // a leg is ringing (or auto-answering)
//   tring.on('call:answered', ({call}) => …);
//   tring.on('call:ended',    ({call}) => …);
//
// The agent holds ONE authenticated WebSocket to the media gateway (/agent/ws)
// for presence + call signaling, and peers over raw WebRTC (ADR-001). The agent
// never speaks SIP and FreeSWITCH stays private.
//
// The agent holds a presence socket to the media plane (/rtc, internal/media);
// an IVR transfer (or web/WhatsApp call) rings them, and on accept they join the
// caller's room over WebRTC. Call control (hold/resume/transfer) is issued on the
// call socket and actuated by the orchestrator — no Tring API call needed.
//
// NOTE: backend click-to-call to a PSTN number is NOT yet wired on this plane (it
// needs the FreeSWITCH PstnGateway). Today this surfaces IVR-transfer / web /
// WhatsApp calls — all pion. The SDK still cannot ORIGINATE calls: Tring.startCall
// only POSTs to the integrator's own backend, which originates via the API.

import { Emitter } from "./events";
import { RtcAgent, type RtcCall } from "./rtc";
import type { Call, CallStatus, InitOpts, TringEvent } from "./types";

// The most recently initialised agent, for the static Tring.startCall (the agent.js
// widget owns its instance, so integrators can't reach it any other way).
let current: Tring | null = null;
let calling = false;

export class Tring extends Emitter<TringEvent> {
  private readonly agent: RtcAgent;
  private readonly autoAnswer: boolean;
  private ringing: Call | null = null; // the not-yet-accepted inbound call, if any
  /** The backend's Away/Break flag for this agent; false = Away (won't be rung). */
  available = true;
  private online = false;

  static async init(opts: InitOpts): Promise<Tring> {
    const t = new Tring(opts);
    await t.connect();
    current = t;
    return t;
  }

  /**
   * Click-to-call from your CRM's own button:
   *
   *   Tring.startCall('/crm/call?id=abc', { ticket: 'T-99' })
   *
   * POSTs to `url` — YOUR backend endpoint, which looks up the number and
   * originates via POST /v1/calls — with same-origin cookies, an X-CSRF-Token
   * header from <meta name="csrf-token"> when present, and `data` as a JSON
   * body. Pass IDs, never a number. Rejects when the agent isn't connected (the
   * call couldn't ring them), while another Tring.startCall is still in flight, or
   * when your endpoint returns a non-2xx status; resolves with its Response.
   */
  static async startCall(url: string, data?: Record<string, unknown>): Promise<Response> {
    if (!current?.online) throw new Error("Tring.startCall: agent is not connected");
    if (calling) throw new Error("Tring.startCall: a call request is already in flight");
    calling = true;
    try {
      const headers: Record<string, string> = {};
      const csrf = document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content;
      if (csrf) headers["X-CSRF-Token"] = csrf;
      if (data) headers["Content-Type"] = "application/json";
      const res = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers,
        body: data ? JSON.stringify(data) : undefined,
      });
      if (!res.ok) throw new Error(`Tring.startCall: ${url} returned ${res.status}`);
      return res;
    } finally {
      calling = false;
    }
  }

  private constructor(private readonly opts: InitOpts) {
    super();
    if (!opts.gatewayUrl) {
      throw new Error("Tring.init: gatewayUrl is required");
    }
    this.autoAnswer = opts.autoAnswer ?? true;
    const getToken =
      opts.getToken ?? (opts.token ? () => Promise.resolve(opts.token as string) : undefined);
    if (!getToken) {
      throw new Error("Tring.init: provide `getToken` (preferred) or `token`");
    }
    this.agent = new RtcAgent({
      // Display name for the room join; the real identity rides in the JWT.
      gatewayUrl: opts.gatewayUrl,
      name: "agent",
      getToken,
      iceServers: opts.iceServers,
    });
    this.agent.on("online", () => {
      // The gateway sends `availability` right after `ready` only when Away, so
      // each (re)connect starts from available.
      this.available = true;
      this.online = true;
      this.emit({ type: "registered" });
    });
    this.agent.on("availability", ({ available }) => {
      this.available = available;
      this.emit({ type: "availability", available });
    });
    this.agent.on("reconnecting", () => {
      this.online = false;
      this.emit({ type: "reconnecting", attempt: 0 });
    });
    this.agent.on("offline", () => {
      this.online = false;
      this.emit({ type: "unregistered" });
    });
    this.agent.on("error", ({ error }) => this.emit({ type: "error", error }));
    this.agent.on("incoming", ({ from }) => this.handleIncoming(from));
    // The server withdrew an unanswered ring (round-robin/sequential advanced to
    // the next agent, or someone else answered). End the ringing call so the UI
    // dismisses — it never connected.
    this.agent.on("incoming-canceled", () => {
      // ONLY dismiss a still-ringing, not-yet-accepted call. Never touch an active
      // call: the server's catch-all cancel fires on answer too, and it reaches the
      // agent who just accepted — ending their live call here would be a bug.
      const call = this.ringing;
      if (!call || call.status !== "ringing") return;
      this.ringing = null;
      call.status = "ended";
      this.emit({ type: "call:ended", call });
    });
  }

  private async connect(): Promise<void> {
    await this.agent.goOnline();
  }

  async disconnect(): Promise<void> {
    if (current === this) current = null;
    this.agent.goOffline();
  }

  // handleIncoming surfaces a ringing call. On accept the agent joins the room
  // (RtcCall); call control delegates to that live call.
  private handleIncoming(from?: string): void {
    const label = from || "";
    let rtc: RtcCall | null = null;
    let muted = false;

    const call: Call = {
      to: label,
      direction: "inbound",
      displayName: label || undefined,
      status: "ringing" as CallStatus,
      accept: async () => {
        if (rtc) return;
        // Committing to this call — drop the ringing ref so a late incoming-cancel
        // (e.g. the server's answer catch-all) can't end the call we're answering.
        this.ringing = null;
        try {
          rtc = await this.agent.accept();
        } catch (err) {
          // Answering failed before the call socket came up (token mint, mic).
          // Surface it and end the ring — the widget's Accept handler swallows
          // the rejection, so without this the panel would hang on "Connecting…".
          call.status = "ended";
          this.emit({ type: "error", error: err as Error });
          this.emit({ type: "call:ended", call });
          throw err;
        }
        if (!rtc) return;
        rtc.on("connected", () => {
          call.status = "connected";
          this.emit({ type: "call:answered", call });
        });
        rtc.on("ended", () => {
          call.status = "ended";
          this.emit({ type: "call:ended", call });
        });
        rtc.on("error", ({ error }) => this.emit({ type: "error", error }));
        rtc.on("supervisor", ({ present, mode, name }) =>
          this.emit({ type: "call:supervisor", call, present, mode, name }),
        );
        // The remote party's screen-share arrives as a video track; surface it,
        // and clear it when that track ends (they stopped sharing).
        rtc.on("track", ({ kind, stream, track }) => {
          if (kind !== "video") return;
          this.emit({ type: "call:remote-video", call, stream });
          track.onended = () => this.emit({ type: "call:remote-video", call, stream: null });
        });
      },
      hangup: async () => {
        if (rtc) {
          rtc.hangup();
        } else {
          // Declining a ringing call (never answered) → clear it and surface the
          // end so the widget hides the panel.
          this.agent.decline();
          call.status = "ended";
          this.emit({ type: "call:ended", call });
        }
      },
      sendDTMF: async (digit: string) => {
        rtc?.sendDtmf(digit);
      },
      setMuted: (m: boolean) => {
        muted = m;
        rtc?.setMuted(m);
      },
      get muted() {
        return muted;
      },
      hold: () => rtc?.hold(),
      resume: () => rtc?.resume(),
      transfer: (target) => rtc?.transfer(target),
      shareScreen: (constraints) =>
        rtc ? rtc.shareScreen(constraints) : Promise.reject(new Error("call not active")),
      stopScreen: () => rtc?.stopScreen(),
      get sharingScreen() {
        return rtc?.sharingScreen ?? false;
      },
    };

    this.ringing = call;
    this.emit({ type: "call:ringing", call });
    if (this.autoAnswer) {
      void call.accept().catch((err) => this.emit({ type: "error", error: err as Error }));
    }
  }
}
