// AgentGateway — the agent side of the media gateway (ADR-001 M4b).
//
// An agent's browser holds ONE WebSocket to the gateway (/agent/ws), authed
// with the agent JWT. Presence lives on that socket. Every call — the agent's
// own outbound leg AND a true inbound (IVR transfer) — arrives as an `incoming`
// message; the browser answers by creating a WebRTC offer and posting it back
// over the socket, the gateway replies with an `answer`, and audio flows
// browser ↔ gateway ↔ (private) FreeSWITCH. No SIP, no WSS-to-FS, no sip.js.
//
// This replaces SipAdapter for the agent path. DTMF rides RFC 4733 via the
// RTCDTMFSender; mute toggles the mic track.

// No ICE servers by default: the gateway is a public server, so the browser
// reaches it on its host candidate without STUN (ADR-001). Pass `iceServers`
// (e.g. a TURN entry) for UDP-blocked clients.
const DEFAULT_ICE: RTCIceServer[] = [];

export type AgentCallState = "connected" | "ended";

/** One ringing/active call surfaced to the Tring controller. */
export interface AgentCall {
  readonly callId: string;
  /** Agent-facing label for the call (e.g. a ticket ref). Never the dialed
   *  number — the backend keeps that. May be empty (generic "Incoming call"). */
  readonly from: string;
  /** True when the gateway flagged this as a backend click-to-call the agent
   *  already initiated → auto-answer rather than ring Accept/Decline. */
  readonly auto: boolean;
  /** Accept: capture mic, offer WebRTC to the gateway. Resolves once sent. */
  accept(): Promise<void>;
  /** Decline a still-ringing call. */
  reject(): Promise<void>;
  /** End an accepted/active call. */
  hangup(): Promise<void>;
  sendDTMF(digit: string): void;
  setMuted(muted: boolean): void;
  /** Register the lifecycle callback (connected → ended). */
  onState(cb: (s: AgentCallState) => void): void;
}

export interface AgentGatewayHandlers {
  /** WS up + presence registered. */
  onReady(): void;
  /** A call is ringing for this agent. */
  onIncoming(call: AgentCall): void;
  /** WS closed (network drop / server shutdown). */
  onClosed(reason?: string): void;
}

export interface AgentGatewayOpts {
  /** Gateway base URL, e.g. "https://gw.tring.io" (ws(s) is derived). */
  gatewayUrl: string;
  /** Agent JWT (sent as the `token` query param — WS can't set headers). */
  token: string;
  iceServers?: RTCIceServer[];
}

export class AgentGateway {
  private ws: WebSocket | null = null;
  private readonly calls = new Map<string, GatewayCall>();
  private readyOnce: (() => void) | null = null;

  constructor(
    private readonly opts: AgentGatewayOpts,
    private readonly h: AgentGatewayHandlers,
  ) {}

  /** Open the signaling socket. Resolves on the first `ready` message. */
  connect(): Promise<void> {
    const url = `${wsBase(this.opts.gatewayUrl)}/agent/ws?token=${encodeURIComponent(this.opts.token)}`;
    return new Promise((resolve, reject) => {
      let opened = false;
      const ws = new WebSocket(url);
      this.ws = ws;
      this.readyOnce = () => {
        opened = true;
        resolve();
      };
      ws.onmessage = (ev) => this.onMessage(ev);
      ws.onerror = () => {
        if (!opened) reject(new Error("agent gateway socket failed"));
      };
      ws.onclose = () => {
        for (const c of this.calls.values()) c.markEnded("socket closed");
        this.calls.clear();
        this.h.onClosed();
      };
    });
  }

  disconnect(): void {
    for (const c of this.calls.values()) c.teardown();
    this.calls.clear();
    this.ws?.close();
    this.ws = null;
  }

  // send serializes one JSON envelope to the gateway.
  send(msg: Record<string, unknown>): void {
    this.ws?.send(JSON.stringify(msg));
  }

  private onMessage(ev: MessageEvent): void {
    let m: { type: string; call_id?: string; sdp?: string; from?: string; reason?: string; auto?: boolean };
    try {
      m = JSON.parse(ev.data as string);
    } catch {
      return;
    }
    switch (m.type) {
      case "ready":
        this.readyOnce?.();
        this.readyOnce = null;
        this.h.onReady();
        break;
      case "incoming": {
        if (!m.call_id) return;
        const call = new GatewayCall(
          m.call_id,
          m.from ?? "",
          !!m.auto,
          this,
          this.opts.iceServers ?? DEFAULT_ICE,
        );
        this.calls.set(m.call_id, call);
        this.h.onIncoming(call);
        break;
      }
      case "answer":
        if (m.call_id && m.sdp) void this.calls.get(m.call_id)?.applyAnswer(m.sdp);
        break;
      case "ended":
        if (m.call_id) {
          this.calls.get(m.call_id)?.markEnded(m.reason);
          this.calls.delete(m.call_id);
        }
        break;
    }
  }

  forget(callId: string): void {
    this.calls.delete(callId);
  }
}

// GatewayCall owns one call's WebRTC peer + media elements.
class GatewayCall implements AgentCall {
  private pc: RTCPeerConnection | null = null;
  private mic: MediaStream | null = null;
  private audioEl: HTMLAudioElement | null = null;
  private dtmf: RTCDTMFSender | null = null;
  private stateCb: ((s: AgentCallState) => void) | null = null;
  private ended = false;
  private _muted = false;

  constructor(
    readonly callId: string,
    readonly from: string,
    readonly auto: boolean,
    private readonly gw: AgentGateway,
    private readonly iceServers: RTCIceServer[],
  ) {}

  onState(cb: (s: AgentCallState) => void): void {
    this.stateCb = cb;
  }

  async accept(): Promise<void> {
    if (this.pc || this.ended) return; // already accepting/active — ignore
    const pc = new RTCPeerConnection({ iceServers: this.iceServers });
    this.pc = pc;

    pc.onconnectionstatechange = () => {
      switch (pc.connectionState) {
        case "connected":
          this.stateCb?.("connected");
          break;
        case "failed":
        case "closed":
        case "disconnected":
          this.markEnded(pc.connectionState);
          break;
      }
    };
    pc.ontrack = (e) => {
      const el = this.ensureAudioEl();
      el.srcObject = e.streams[0] ?? new MediaStream([e.track]);
      void el.play().catch(() => {});
    };

    this.mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    const track = this.mic.getAudioTracks()[0];
    if (!track) throw new Error("no microphone track available");
    const sender = pc.addTrack(track, this.mic);
    this.dtmf = sender.dtmf ?? null;

    const offer = await pc.createOffer({ offerToReceiveAudio: true });
    await pc.setLocalDescription(offer);
    await waitForIce(pc);

    this.gw.send({ type: "offer", call_id: this.callId, sdp: pc.localDescription?.sdp ?? "" });
  }

  async applyAnswer(sdp: string): Promise<void> {
    await this.pc?.setRemoteDescription({ type: "answer", sdp });
  }

  async reject(): Promise<void> {
    this.gw.send({ type: "reject", call_id: this.callId });
    // markEnded fires the "ended" state (so the UI closes), then tears down +
    // deregisters — same as hangup. Previously this only tore down silently, so
    // a declined ringing call left the panel open.
    this.markEnded("rejected");
  }

  async hangup(): Promise<void> {
    this.gw.send({ type: "hangup", call_id: this.callId });
    this.markEnded("hangup");
  }

  sendDTMF(digit: string): void {
    this.dtmf?.insertDTMF(digit, 160);
  }

  setMuted(muted: boolean): void {
    this._muted = muted;
    this.mic?.getAudioTracks().forEach((t) => (t.enabled = !muted));
  }

  get muted(): boolean {
    return this._muted;
  }

  // markEnded fires the ended callback once, then tears down + deregisters.
  markEnded(_reason?: string): void {
    if (this.ended) return;
    this.ended = true;
    this.stateCb?.("ended");
    this.teardown();
    this.gw.forget(this.callId);
  }

  teardown(): void {
    this.mic?.getTracks().forEach((t) => t.stop());
    this.mic = null;
    this.dtmf = null;
    if (this.pc) {
      this.pc.onconnectionstatechange = null;
      this.pc.ontrack = null;
      try {
        this.pc.close();
      } catch {
        /* already closed */
      }
      this.pc = null;
    }
    if (this.audioEl) {
      this.audioEl.srcObject = null;
      this.audioEl.remove();
      this.audioEl = null;
    }
  }

  private ensureAudioEl(): HTMLAudioElement {
    if (this.audioEl) return this.audioEl;
    const el = document.createElement("audio");
    el.autoplay = true;
    el.hidden = true;
    document.body.appendChild(el);
    this.audioEl = el;
    return el;
  }
}

// wsBase turns an http(s) gateway URL into its ws(s) origin (no trailing slash).
function wsBase(httpUrl: string): string {
  return httpUrl.replace(/\/$/, "").replace(/^http/, "ws");
}

// waitForIce blocks until ICE gathering completes (non-trickle: we send the
// full SDP once), with a safety timeout.
function waitForIce(pc: RTCPeerConnection): Promise<void> {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const done = () => {
      pc.removeEventListener("icegatheringstatechange", check);
      resolve();
    };
    const check = () => {
      if (pc.iceGatheringState === "complete") done();
    };
    pc.addEventListener("icegatheringstatechange", check);
    setTimeout(done, 2000);
  });
}
