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
  /** WS up + presence registered. Fires on every (re)connect. */
  onReady(): void;
  /** A call is ringing for this agent. */
  onIncoming(call: AgentCall): void;
  /** WS closed for good (explicit disconnect). */
  onClosed(reason?: string): void;
  /** A drop was detected and a reconnect is scheduled (1-based attempt). */
  onReconnecting?(attempt: number): void;
}

export interface AgentGatewayOpts {
  /** Gateway base URL, e.g. "https://gw.tring.io" (ws(s) is derived). */
  gatewayUrl: string;
  /** Mint a fresh agent JWT. Called on every (re)connect, so an expired token
   *  is never reused — the agent JWT is short-lived (~5 min). For a static
   *  token, pass `() => Promise.resolve(token)`. */
  getToken: () => Promise<string>;
  iceServers?: RTCIceServer[];
}

// Keepalive must beat the gateway's 90s read deadline; backoff is capped so a
// flapping gateway doesn't get hammered.
const PING_INTERVAL_MS = 30_000;
const RECONNECT_BASE_MS = 1_000;
const RECONNECT_MAX_MS = 30_000;

export class AgentGateway {
  private ws: WebSocket | null = null;
  private readonly calls = new Map<string, GatewayCall>();
  private readyOnce: (() => void) | null = null;
  private closedByUser = false;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempt = 0;

  constructor(
    private readonly opts: AgentGatewayOpts,
    private readonly h: AgentGatewayHandlers,
  ) {}

  /** Open the signaling socket. Resolves on the first `ready`. After a
   *  successful first connect, drops auto-reconnect with backoff. */
  connect(): Promise<void> {
    this.closedByUser = false;
    return new Promise((resolve, reject) => {
      this.readyOnce = resolve;
      void this.open(reject);
    });
  }

  // open mints a token, opens the WS, and wires keepalive + reconnect. `reject`
  // is passed only on the initial connect so a first-attempt failure surfaces
  // to the caller; later drops reconnect silently instead.
  private async open(reject?: (e: Error) => void): Promise<void> {
    let token: string;
    try {
      token = await this.opts.getToken();
    } catch (e) {
      if (reject) reject(e as Error);
      else this.scheduleReconnect();
      return;
    }
    const url = `${wsBase(this.opts.gatewayUrl)}/agent/ws?token=${encodeURIComponent(token)}`;
    const ws = new WebSocket(url);
    this.ws = ws;
    let opened = false;
    ws.onopen = () => {
      opened = true;
      this.reconnectAttempt = 0;
      this.startPing();
    };
    ws.onmessage = (ev) => this.onMessage(ev);
    ws.onerror = () => {
      /* surfaced via onclose */
    };
    ws.onclose = () => {
      this.stopPing();
      for (const c of this.calls.values()) c.markEnded("socket closed");
      this.calls.clear();
      this.ws = null;
      if (this.closedByUser) {
        this.h.onClosed("disconnected");
        return;
      }
      if (reject && !opened) {
        reject(new Error("agent gateway socket failed")); // initial attempt failed
        return;
      }
      this.scheduleReconnect(); // dropped after a good connect → retry
    };
  }

  private scheduleReconnect(): void {
    if (this.closedByUser || this.reconnectTimer) return;
    const attempt = this.reconnectAttempt++;
    const base = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** attempt);
    const delay = base + base * 0.3 * Math.random(); // jitter so clients don't sync
    this.h.onReconnecting?.(attempt + 1);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.open();
    }, delay);
  }

  private startPing(): void {
    this.stopPing();
    this.pingTimer = setInterval(() => this.send({ type: "ping" }), PING_INTERVAL_MS);
  }

  private stopPing(): void {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }

  disconnect(): void {
    this.closedByUser = true;
    this.stopPing();
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    for (const c of this.calls.values()) c.teardown();
    this.calls.clear();
    this.ws?.close();
    this.ws = null;
  }

  // send serializes one JSON envelope to the gateway (no-op if not open).
  send(msg: Record<string, unknown>): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
    }
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
