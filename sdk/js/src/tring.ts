// Public entry point: the Tring class (agent calling over the media gateway).
//
// Usage:
//
//   const tring = await Tring.init({
//     token:      '<jwt from /v1/agents/{id}/token>',
//     apiBase:    'http://localhost:8085',
//     gatewayUrl: 'http://localhost:8087',
//   });
//   tring.on('registered', () => console.log('ready'));
//   const call = await tring.call('+919876543210');
//   call.hangup();
//
// The agent holds one authenticated WebSocket to the media gateway (/agent/ws)
// for presence + call signaling and peers over raw WebRTC (ADR-001). The agent
// never speaks SIP and FreeSWITCH stays private.

import { AgentGateway, type AgentCall } from "./agent-gateway";
import { ApiClient } from "./api";
import { Emitter } from "./events";
import type { Call, CallStatus, InitOpts, TringEvent } from "./types";

export class Tring extends Emitter<TringEvent> {
  private readonly api: ApiClient;
  private readonly gw: AgentGateway;
  private readonly autoAnswer: boolean;
  private pendingCall: {
    to: string;
    resolve: (c: Call) => void;
    reject: (e: Error) => void;
    cancelTimeout: () => void;
  } | null = null;

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
    this.api = new ApiClient(opts.apiBase, opts.token);
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

  /**
   * Place an outbound call. The backend originates; FreeSWITCH delivers the
   * agent's leg to the gateway, which signals this browser via `incoming` —
   * we accept it (offer WebRTC) and audio flows.
   *
   * If a previous call() is still waiting for its leg, it is cancelled
   * (resolves with a "superseded" error) before the new one is started.
   *
   * Timeout: 5s from originate ACK to the incoming leg. The backend responds
   * within ~1s on the happy path; longer is a real failure (agent not
   * reachable, trunk rejected, etc.).
   */
  async call(to: string): Promise<Call> {
    if (this.pendingCall) {
      this.pendingCall.cancelTimeout();
      this.pendingCall.reject(new Error("superseded by a newer call"));
      this.pendingCall = null;
    }

    await this.api.originate(to);

    return new Promise<Call>((resolve, reject) => {
      const timeout = setTimeout(() => {
        if (!this.pendingCall || this.pendingCall.to !== to) return;
        this.pendingCall = null;
        const err = new Error("no incoming leg from the gateway within 5s");
        this.emit({ type: "error", error: err });
        reject(err);
      }, 5_000);

      this.pendingCall = {
        to,
        resolve: (c) => {
          clearTimeout(timeout);
          resolve(c);
        },
        reject: (e) => {
          clearTimeout(timeout);
          reject(e);
        },
        cancelTimeout: () => clearTimeout(timeout),
      };
    });
  }

  async disconnect(): Promise<void> {
    this.gw.disconnect();
  }

  // handleIncoming: a pending call() means WE originated (the agent's own leg
  // — auto-accept); otherwise it's a true inbound (e.g. an IVR transfer) gated
  // on autoAnswer.
  private handleIncoming(ac: AgentCall): void {
    const outbound = !!this.pendingCall;
    const to = this.pendingCall?.to ?? ac.from ?? "";

    const call: Call = {
      to,
      direction: outbound ? "outbound" : "inbound",
      displayName: ac.from || undefined,
      status: "ringing" as CallStatus,
      accept: async () => {
        await ac.accept();
      },
      hangup: async () => {
        if (outbound || !this.pendingCall) await ac.hangup();
        else await ac.reject(); // still ringing, never accepted
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
    this.pendingCall?.resolve(call);
    this.pendingCall = null;

    // Auto-answer the agent's OWN outbound leg always; auto-answer a true
    // inbound only when autoAnswer is on.
    if (outbound || this.autoAnswer) {
      ac.accept().catch((err) => {
        this.emit({ type: "error", error: err as Error });
      });
    }
  }
}
