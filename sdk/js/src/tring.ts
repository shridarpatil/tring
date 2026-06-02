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
   *
   * If a previous call() is still waiting for its INVITE, it is cancelled
   * (resolves with a "superseded" error) before the new one is started —
   * a rapid second click should not produce a phantom timeout log later.
   *
   * Timeout: 5s from originate ACK to inbound INVITE. FreeSWITCH responds
   * within ~1s on the happy path; anything longer is a real failure (no
   * registered contact, trunk rejected, etc.).
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
        // Only fire once; defend against late timeouts after pendingCall
        // was already cleared by handleInvitation.
        if (!this.pendingCall || this.pendingCall.to !== to) return;
        this.pendingCall = null;
        const err = new Error("no INVITE received from FreeSWITCH within 5s");
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
    await this.sip.disconnect();
  }

  // ---- internals ----------------------------------------------------

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
