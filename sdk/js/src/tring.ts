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
// WhatsApp calls — all pion. The SDK still cannot ORIGINATE calls.

import { Emitter } from "./events";
import { RtcAgent, type RtcCall } from "./rtc";
import type { Call, CallStatus, InitOpts, TringEvent } from "./types";

export class Tring extends Emitter<TringEvent> {
  private readonly agent: RtcAgent;
  private readonly autoAnswer: boolean;

  static async init(opts: InitOpts): Promise<Tring> {
    const t = new Tring(opts);
    await t.connect();
    return t;
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
    this.agent.on("online", () => this.emit({ type: "registered" }));
    this.agent.on("reconnecting", () => this.emit({ type: "reconnecting", attempt: 0 }));
    this.agent.on("offline", () => this.emit({ type: "unregistered" }));
    this.agent.on("error", ({ error }) => this.emit({ type: "error", error }));
    this.agent.on("incoming", ({ from }) => this.handleIncoming(from));
  }

  private async connect(): Promise<void> {
    await this.agent.goOnline();
  }

  async disconnect(): Promise<void> {
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
        rtc = await this.agent.accept();
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

    this.emit({ type: "call:ringing", call });
    if (this.autoAnswer) {
      void call.accept().catch((err) => this.emit({ type: "error", error: err as Error }));
    }
  }
}
