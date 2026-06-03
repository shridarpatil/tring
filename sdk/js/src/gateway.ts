// GatewayAdapter — raw WebRTC to the Tring media gateway (ADR-001).
//
// The browser peers directly with the gateway over WebRTC: it offers its mic,
// the gateway answers after bridging into a (private) FreeSWITCH leg, and audio
// flows browser ↔ gateway ↔ FS. Signaling is a single HTTPS POST of the SDP
// offer (non-trickle ICE) to `${gatewayUrl}/webrtc/offer`; the response carries
// the SDP answer. No SIP, no WebSocket, no FreeSWITCH exposure.
//
// This replaces SipAdapter (sip.js over WSS) for the web-calling path. DTMF
// rides RFC 4733 telephone-event via the RTCDTMFSender on the audio track.

export interface GatewayCallOpts {
  /** Gateway base URL, e.g. "https://gw.tring.io" (no trailing /webrtc/offer). */
  gatewayUrl: string;
  /** IVR flow UUID to pin this call to. Empty → the gateway's default route. */
  flowId?: string;
  /** Optional agent token; sent as `Authorization: Bearer` when the gateway requires auth. */
  token?: string;
  /** ICE servers for the peer connection. Defaults to a public STUN server. */
  iceServers?: RTCIceServer[];
  /** Lifecycle callback: ringing (offer sent) → connected (media up) → ended. */
  onState?: (state: "ringing" | "connected" | "ended") => void;
}

const DEFAULT_ICE: RTCIceServer[] = [
  { urls: "stun:stun.l.google.com:19302" },
];

export class GatewayAdapter {
  private pc: RTCPeerConnection | null = null;
  private mic: MediaStream | null = null;
  private audioEl: HTMLAudioElement | null = null;
  private dtmf: RTCDTMFSender | null = null;
  private _muted = false;
  private ended = false;

  get muted(): boolean {
    return this._muted;
  }

  get active(): boolean {
    return this.pc !== null;
  }

  /**
   * Acquire the mic, negotiate WebRTC with the gateway, and start media.
   * Resolves once the SDP answer is applied (the call is then connecting →
   * connected via onState). Throws on mic denial or signaling failure.
   */
  async start(opts: GatewayCallOpts): Promise<void> {
    const pc = new RTCPeerConnection({
      iceServers: opts.iceServers ?? DEFAULT_ICE,
    });
    this.pc = pc;

    pc.onconnectionstatechange = () => {
      switch (pc.connectionState) {
        case "connected":
          opts.onState?.("connected");
          break;
        case "failed":
        case "closed":
        case "disconnected":
          this.finish(opts);
          break;
      }
    };

    // Play whatever the gateway sends back (FS audio).
    pc.ontrack = (ev) => {
      const el = this.ensureAudioEl();
      el.srcObject = ev.streams[0] ?? new MediaStream([ev.track]);
      void el.play().catch(() => {});
    };

    // Mic → first (and only) audio sender; capture its DTMF channel.
    this.mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    const track = this.mic.getAudioTracks()[0];
    if (!track) throw new Error("no microphone track available");
    const sender = pc.addTrack(track, this.mic);
    this.dtmf = sender.dtmf ?? null;

    const offer = await pc.createOffer({ offerToReceiveAudio: true });
    await pc.setLocalDescription(offer);
    await this.waitForIce(pc);

    opts.onState?.("ringing");

    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    if (opts.token) headers["Authorization"] = `Bearer ${opts.token}`;

    const res = await fetch(`${opts.gatewayUrl.replace(/\/$/, "")}/webrtc/offer`, {
      method: "POST",
      headers,
      body: JSON.stringify({
        sdp: pc.localDescription?.sdp ?? "",
        flow_id: opts.flowId ?? "",
      }),
    });
    if (!res.ok) {
      await this.stop();
      throw new Error(`gateway offer rejected (${res.status})`);
    }
    const { sdp } = (await res.json()) as { sdp: string };
    await pc.setRemoteDescription({ type: "answer", sdp });
  }

  /** Send a DTMF digit (0-9, *, #) via RFC 4733 telephone-event. */
  async sendDTMF(digit: string): Promise<void> {
    // insertDtmf is synchronous in the WebRTC API; keep the async signature so
    // it's a drop-in for the old SIP INFO path.
    this.dtmf?.insertDTMF(digit, 160);
  }

  /** Mute/unmute the local mic by toggling the track. */
  setMuted(muted: boolean): void {
    this._muted = muted;
    this.mic?.getAudioTracks().forEach((t) => {
      t.enabled = !muted;
    });
  }

  /** Tear down the peer connection, mic, and audio element. Idempotent. */
  async stop(): Promise<void> {
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

  // finish fires the ended callback once, then tears down.
  private finish(opts: GatewayCallOpts): void {
    if (this.ended) return;
    this.ended = true;
    opts.onState?.("ended");
    void this.stop();
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

  // waitForIce blocks until ICE gathering completes (non-trickle: we POST the
  // full SDP once), with a safety timeout so a stuck candidate doesn't hang the
  // call forever.
  private waitForIce(pc: RTCPeerConnection): Promise<void> {
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
}
