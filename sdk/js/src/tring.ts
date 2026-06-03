// Public entry point: the Tring class.
//
// Usage:
//
//   const tring = await Tring.init({
//     token:   '<jwt from /v1/agents/{id}/token>',
//     apiBase: 'http://localhost:8085',
//     gatewayUrl: 'http://localhost:8087',  // ADR-001 M4b — raw WebRTC
//   });
//   tring.on('registered', () => console.log('ready'));
//   const call = await tring.call('+919876543210');
//   call.hangup();
//
// Two transports share one public API:
//   - gatewayUrl set → AgentGateway (WebSocket signaling + raw WebRTC to the
//     media gateway; the agent never speaks SIP, FreeSWITCH stays private).
//   - otherwise      → SipAdapter (legacy sip.js over WSS to FreeSWITCH).

import { Invitation, SessionState } from "sip.js";

import { AgentGateway, type AgentCall } from "./agent-gateway";
import { ApiClient } from "./api";
import { Emitter } from "./events";
import { SipAdapter } from "./sip";
import type {
  Call,
  CallStatus,
  InitOpts,
  TringEvent,
  SipCredentialsResponse,
} from "./types";

export class Tring extends Emitter<TringEvent> {
  private readonly api: ApiClient;
  private readonly sip: SipAdapter | null;
  private readonly gw: AgentGateway | null;
  private readonly autoAnswer: boolean;
  private credentials: SipCredentialsResponse | null = null;
  private currentSession: Invitation | null = null;
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
    this.autoAnswer = opts.autoAnswer ?? true;
    this.api = new ApiClient(opts.apiBase, opts.token);

    if (opts.gatewayUrl) {
      // Gateway transport: one WebSocket carries presence + incoming calls.
      this.sip = null;
      this.gw = new AgentGateway(
        {
          gatewayUrl: opts.gatewayUrl,
          token: opts.token,
          iceServers: opts.iceServers,
        },
        {
          onReady: () => this.emit({ type: "registered" }),
          onIncoming: (c) => this.handleGatewayIncoming(c),
          onClosed: (reason) => this.emit({ type: "unregistered", reason }),
        },
      );
    } else {
      // Legacy SIP transport (removed in M5).
      this.gw = null;
      this.sip = new SipAdapter({
        onInvitation: (i) => this.handleInvitation(i),
        onRegistered: () => this.emit({ type: "registered" }),
        onUnregistered: (reason) => this.emit({ type: "unregistered", reason }),
      });
    }
  }

  private async connect(): Promise<void> {
    if (this.gw) {
      await this.gw.connect();
      return;
    }
    this.credentials = await this.api.fetchCredentials();
    await this.sip!.connect(this.credentials);
  }

  /**
   * Place an outbound call. The backend originates; the call comes back to
   * this browser — as an inbound INVITE (SIP) or an `incoming` signal
   * (gateway) — which we accept, and audio flows.
   *
   * If a previous call() is still waiting for its leg, it is cancelled
   * (resolves with a "superseded" error) before the new one is started.
   *
   * Timeout: 5s from originate ACK to the inbound leg. The backend responds
   * within ~1s on the happy path; longer is a real failure (agent not
   * reachable, trunk rejected, etc.).
   */
  async call(to: string): Promise<Call> {
    // Cancel an in-flight pending call before starting a new one.
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
        const err = new Error("no inbound leg received from the backend within 5s");
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
    if (this.gw) {
      this.gw.disconnect();
      return;
    }
    await this.sip!.disconnect();
  }

  // ---- gateway transport --------------------------------------------

  // handleGatewayIncoming mirrors handleInvitation for the WebSocket path: a
  // pending call() means WE originated (the agent's own leg — auto-accept);
  // otherwise it's a true inbound (e.g. an IVR transfer) gated on autoAnswer.
  private handleGatewayIncoming(ac: AgentCall): void {
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

  // ---- sip transport ------------------------------------------------

  private handleInvitation(inv: Invitation): void {
    this.currentSession = inv;
    // A pending call() means WE originated — this INVITE is the agent's own
    // leg of an outbound call. No pending call means a call was bridged TO
    // the agent (e.g. an IVR transfer) — a genuine inbound call to answer.
    const outbound = !!this.pendingCall;
    const to = this.pendingCall?.to ?? inv.remoteIdentity.uri.user ?? "";

    const call: Call = {
      to,
      direction: outbound ? "outbound" : "inbound",
      displayName: inv.remoteIdentity.displayName || undefined,
      status: "ringing" as CallStatus,
      accept: async () => {
        if (inv.state === SessionState.Initial) {
          await inv.accept();
        }
      },
      hangup: async () => {
        const s = inv.state;
        if (s === SessionState.Established) await inv.bye();
        else if (s === SessionState.Initial || s === SessionState.Establishing)
          await inv.reject();
      },
      sendDTMF: async (digit: string) => {
        await inv.info({
          requestOptions: {
            body: {
              contentDisposition: "render",
              contentType: "application/dtmf-relay",
              content: `Signal=${digit}\r\nDuration=160`,
            },
          },
        });
      },
    };

    this.emit({ type: "call:ringing", call });
    this.pendingCall?.resolve(call);
    this.pendingCall = null;

    inv.stateChange.addListener((state) => {
      switch (state) {
        case SessionState.Established:
          call.status = "connected";
          this.emit({ type: "call:answered", call });
          break;
        case SessionState.Terminated:
          call.status = "ended";
          this.emit({ type: "call:ended", call });
          if (this.currentSession === inv) this.currentSession = null;
          break;
      }
    });

    // Auto-answer the agent's OWN outbound leg always; auto-answer a true
    // inbound call only when autoAnswer is on. With autoAnswer off, an
    // inbound call stays ringing until the UI calls accept().
    if (outbound || this.autoAnswer) {
      inv.accept().catch((err) => {
        this.emit({ type: "error", error: err as Error });
      });
    }
  }
}
