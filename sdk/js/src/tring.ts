// Public entry point: the Tring class.
//
// Usage:
//
//   const tring = await Tring.init({
//     token:   '<jwt from /v1/agents/{id}/token>',
//     apiBase: 'http://localhost:8085',
//   });
//   tring.on('registered', () => console.log('ready'));
//   const call = await tring.call('+919876543210');
//   call.hangup();

import { Invitation, SessionState } from "sip.js";

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
  private readonly sip: SipAdapter;
  private readonly autoAnswer: boolean;
  private credentials: SipCredentialsResponse | null = null;
  private currentSession: Invitation | null = null;
  private pendingCall: { to: string; resolve: (c: Call) => void } | null = null;

  static async init(opts: InitOpts): Promise<Tring> {
    const t = new Tring(opts);
    await t.connect();
    return t;
  }

  private constructor(private readonly opts: InitOpts) {
    super();
    this.autoAnswer = opts.autoAnswer ?? true;
    this.api = new ApiClient(opts.apiBase, opts.token);
    this.sip = new SipAdapter({
      onInvitation: (i) => this.handleInvitation(i),
      onRegistered: () => this.emit({ type: "registered" }),
      onUnregistered: (reason) =>
        this.emit({ type: "unregistered", reason }),
    });
  }

  private async connect(): Promise<void> {
    this.credentials = await this.api.fetchCredentials();
    await this.sip.connect(this.credentials);
  }

  /**
   * Place an outbound call. The backend originates; FreeSWITCH calls back
   * into this browser via an inbound INVITE; we accept it and audio flows.
   */
  async call(to: string): Promise<Call> {
    await this.api.originate(to);

    // The originate will trigger an inbound INVITE within ~1s. Wire a
    // one-shot resolver so we hand back a Call to the caller once the
    // INVITE lands.
    return new Promise<Call>((resolve, reject) => {
      const timeout = setTimeout(() => {
        this.pendingCall = null;
        reject(new Error("timed out waiting for ring"));
      }, 15_000);
      this.pendingCall = {
        to,
        resolve: (c) => {
          clearTimeout(timeout);
          resolve(c);
        },
      };
    });
  }

  async disconnect(): Promise<void> {
    await this.sip.disconnect();
  }

  // ---- internals ----------------------------------------------------

  private handleInvitation(inv: Invitation): void {
    this.currentSession = inv;
    const to = this.pendingCall?.to ?? inv.remoteIdentity.uri.user ?? "";

    const call: Call = {
      to,
      status: "ringing" as CallStatus,
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

    if (this.autoAnswer) {
      inv.accept().catch((err) => {
        this.emit({ type: "error", error: err as Error });
      });
    }
  }
}
