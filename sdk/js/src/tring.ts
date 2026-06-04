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
// IMPORTANT: the SDK has NO Tring API access — it cannot originate calls. To
// place an outbound call, the integrator's BACKEND calls `POST /v1/calls`
// (tenant API key) with the agent + destination; FreeSWITCH then rings this
// agent's leg, which arrives here as an (auto-answering) incoming call. The
// agent's browser never sees the dialed number.

import { AgentGateway, type AgentCall } from "./agent-gateway";
import { Emitter } from "./events";
import type { Call, CallStatus, InitOpts, TringEvent } from "./types";

export class Tring extends Emitter<TringEvent> {
  private readonly gw: AgentGateway;
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
    this.gw = new AgentGateway(
      {
        gatewayUrl: opts.gatewayUrl,
        token: opts.token,
        iceServers: opts.iceServers,
      },
      {
        onReady: () => this.emit({ type: "registered" }),
        onIncoming: (c) => this.handleIncoming(c),
        onClosed: (reason) => this.emit({ type: "unregistered", reason }),
      },
    );
  }

  private async connect(): Promise<void> {
    await this.gw.connect();
  }

  async disconnect(): Promise<void> {
    this.gw.disconnect();
  }

  // handleIncoming surfaces every call leg the gateway rings us with — both a
  // true inbound (IVR transfer / inbound DID) and the agent's own leg of a
  // backend-originated outbound. We can't see the dialed number; `ac.from` is a
  // backend-supplied label (or empty → generic). `ac.auto` means the agent
  // already initiated (click-to-call) → auto-answer.
  private handleIncoming(ac: AgentCall): void {
    const label = ac.from || "";
    let accepted = ac.auto || this.autoAnswer;

    const call: Call = {
      to: label,
      direction: ac.auto ? "outbound" : "inbound",
      displayName: label || undefined,
      status: "ringing" as CallStatus,
      accept: async () => {
        accepted = true;
        await ac.accept();
      },
      hangup: async () => {
        // Accepted (auto, autoAnswer, or a manual accept) → end the live call;
        // still ringing and never accepted → decline.
        if (accepted) await ac.hangup();
        else await ac.reject();
      },
      sendDTMF: async (digit: string) => {
        ac.sendDTMF(digit);
      },
    };

    ac.onState((s) => {
      if (s === "connected") {
        call.status = "connected";
        this.emit({ type: "call:answered", call });
      } else {
        call.status = "ended";
        this.emit({ type: "call:ended", call });
      }
    });

    this.emit({ type: "call:ringing", call });

    if (accepted) {
      ac.accept().catch((err) => {
        this.emit({ type: "error", error: err as Error });
      });
    }
  }
}
