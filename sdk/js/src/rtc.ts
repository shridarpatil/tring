// Headless /rtc client for the pion media plane — the SDK home for the logic
// proven in examples/rtc-test.html (visitor) and examples/rtc-agent.html (agent).
//
// The SFU is the sole offerer: the client sends `answer`/`ice` and receives
// `offer`/`ice` plus peer-* membership events. RtcCall is one call leg (used by a
// visitor AND by an agent who accepted a ring). RtcAgent holds an agent's
// presence socket and produces an RtcCall on accept. Speaks the FROZEN contract
// in rtc-protocol.ts.

import { Emitter } from "./events";
import {
  ClientMsgType,
  ServerMsgType,
  type RtcIceServer,
  type RtcMessage,
} from "./rtc-protocol";

// ── helpers ──────────────────────────────────────────────────────────

/** secureContextOK guards getUserMedia: it needs HTTPS (mobile Safari has no
 *  navigator.mediaDevices on http://). Returns an error message or null. */
function secureContextProblem(): string | null {
  if (typeof window !== "undefined" && !window.isSecureContext) {
    return "Microphone needs HTTPS — load this page over https:// (it won't work over http on mobile).";
  }
  if (!navigator.mediaDevices?.getUserMedia) {
    return "This browser has no microphone access (needs a secure context).";
  }
  return null;
}

/** wsURL turns an http(s) gateway base + token into the /rtc WebSocket URL. */
function wsURL(gatewayUrl: string, token: string): string {
  const base = gatewayUrl.replace(/\/$/, "").replace(/^http/, "ws");
  return `${base}/rtc?token=${encodeURIComponent(token)}`;
}

function toRTCIce(servers?: RtcIceServer[]): RTCIceServer[] {
  return (servers ?? []).map((s) => ({
    urls: s.urls,
    username: s.username,
    credential: s.credential,
  }));
}

// ── RtcCall ──────────────────────────────────────────────────────────

export interface RtcCallOpts {
  /** Gateway base URL, e.g. "https://gw.tring.io". */
  gatewayUrl: string;
  /** Room id to join (un-namespaced; the edge namespaces by tenant). */
  room: string;
  /** Auth token: a gateway call token (visitor) or an agent JWT (agent leg). */
  token: string;
  /** Display name shown to peers. */
  name?: string;
  /** Pin an IVR flow to run in this room (visitor calls). */
  flowId?: string;
  /** Extra ICE servers; the edge also sends its own in `joined`. */
  iceServers?: RTCIceServer[];
  /**
   * Pre-captured mic. Pass this when the mic was acquired earlier in the user
   * gesture (e.g. before a credentials fetch) so iOS Safari grants it; join()
   * then skips its own getUserMedia. Omit to let join() capture the mic.
   */
  mic?: MediaStream;
  /** Reconnect attempts on an unexpected socket drop (default 3; 0 disables). */
  reconnectAttempts?: number;
}

export type RtcCallEvent =
  | { type: "connecting" }
  | { type: "connected" }
  | { type: "track"; kind: "audio" | "video"; stream: MediaStream; track: MediaStreamTrack }
  | { type: "peer-joined"; participantId: string; name?: string }
  | { type: "peer-left"; participantId: string }
  | { type: "reconnecting" }
  | { type: "ended"; reason?: string }
  | { type: "error"; error: Error };

export class RtcCall extends Emitter<RtcCallEvent> {
  private ws: WebSocket | null = null;
  private pc: RTCPeerConnection | null = null;
  private mic: MediaStream | null = null;
  private screen: MediaStream | null = null;
  private screenSender: RTCRtpSender | null = null;
  private sessionToken = "";
  private remoteDescSet = false;
  private pendingIce: RTCIceCandidateInit[] = [];
  private edgeIce: RtcIceServer[] = [];
  private peers = new Set<string>(); // other participants currently in the room
  private audioEls: HTMLAudioElement[] = []; // auto-played remote audio elements
  private attemptsLeft: number;
  private closedByUser = false;

  constructor(private readonly opts: RtcCallOpts) {
    super();
    this.attemptsLeft = opts.reconnectAttempts ?? 3;
  }

  /** join captures the mic and connects. Call SYNCHRONOUSLY inside the user
   *  gesture (before any unrelated await) so iOS Safari grants the mic. */
  async join(): Promise<void> {
    if (this.opts.mic) {
      this.mic = this.opts.mic; // captured earlier in the gesture
    } else {
      const problem = secureContextProblem();
      if (problem) throw new Error(problem);
      try {
        this.mic = await navigator.mediaDevices.getUserMedia({ audio: true });
      } catch (e) {
        throw micError(e);
      }
    }
    this.openSocket();
  }

  private openSocket(): void {
    this.emit({ type: "connecting" });
    const ws = new WebSocket(wsURL(this.opts.gatewayUrl, this.opts.token));
    this.ws = ws;
    ws.onopen = () =>
      this.send({
        type: ClientMsgType.Join,
        room: this.opts.room,
        name: this.opts.name,
        flow_id: this.opts.flowId,
        session_token: this.sessionToken || undefined,
      });
    ws.onmessage = (ev) => this.onMessage(JSON.parse(ev.data) as RtcMessage);
    ws.onclose = () => this.onSocketClose();
    ws.onerror = () => this.emit({ type: "error", error: new Error("signaling socket error") });
  }

  private async onMessage(m: RtcMessage): Promise<void> {
    switch (m.type) {
      case ServerMsgType.Joined:
        this.sessionToken = m.session_token ?? "";
        this.edgeIce = m.ice_servers ?? [];
        this.ensurePC();
        break;
      case ServerMsgType.Offer: {
        const pc = this.ensurePC();
        await pc.setRemoteDescription({ type: "offer", sdp: m.sdp });
        this.remoteDescSet = true;
        for (const c of this.pendingIce.splice(0)) await pc.addIceCandidate(c);
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        this.send({ type: ClientMsgType.Answer, sdp: answer.sdp });
        break;
      }
      case ServerMsgType.ICE:
        if (!m.candidate) break;
        if (this.remoteDescSet && this.pc) await this.pc.addIceCandidate(m.candidate);
        else this.pendingIce.push(m.candidate);
        break;
      case ServerMsgType.PeerJoined:
        this.peers.add(m.participant_id ?? "");
        this.emit({ type: "peer-joined", participantId: m.participant_id ?? "", name: m.name });
        break;
      case ServerMsgType.PeerLeft:
        this.peers.delete(m.participant_id ?? "");
        this.emit({ type: "peer-left", participantId: m.participant_id ?? "" });
        // 1:1 call: when the other party leaves, the call is over. (The IVR is an
        // injected source, not a peer, so it never triggers this — only a real
        // participant leaving does.)
        if (this.peers.size === 0) {
          this.closedByUser = true; // a clean end, not a drop → no reconnect
          this.teardown();
          this.emit({ type: "ended", reason: "peer left" });
        }
        break;
      case ServerMsgType.Ended:
        // Server says the call is over (IVR ended / nobody answered) — terminal,
        // so tear down without reconnecting.
        this.closedByUser = true;
        this.teardown();
        this.emit({ type: "ended", reason: m.reason ?? "ended" });
        break;
      case ServerMsgType.Error:
        this.emit({ type: "error", error: new Error(m.reason ?? "edge error") });
        break;
    }
  }

  private ensurePC(): RTCPeerConnection {
    if (this.pc) return this.pc;
    const pc = new RTCPeerConnection({
      iceServers: [...(this.opts.iceServers ?? []), ...toRTCIce(this.edgeIce)],
    });
    for (const t of this.mic!.getTracks()) pc.addTrack(t, this.mic!);
    pc.onicecandidate = (e) => {
      if (e.candidate) this.send({ type: ClientMsgType.ICE, candidate: e.candidate.toJSON() });
    };
    pc.ontrack = (e) => {
      const stream = e.streams[0] ?? new MediaStream([e.track]);
      // Auto-play incoming AUDIO (the IVR voice, the other party) — a calling SDK
      // must sound the call without the integrator wiring an element. Video (a
      // screen share) is left to the consumer's `track` event to render.
      if (e.track.kind === "audio") {
        const el = document.createElement("audio");
        el.autoplay = true;
        (el as HTMLAudioElement & { playsInline?: boolean }).playsInline = true;
        el.srcObject = stream;
        document.body.appendChild(el);
        void el.play().catch(() => {
          /* autoplay may need a gesture; the call UI's tap usually satisfies it */
        });
        this.audioEls.push(el);
        e.track.onended = () => el.remove();
      }
      this.emit({ type: "track", kind: e.track.kind as "audio" | "video", stream, track: e.track });
    };
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === "connected") {
        this.attemptsLeft = this.opts.reconnectAttempts ?? 3; // healthy → reset budget
        this.emit({ type: "connected" });
      }
    };
    this.pc = pc;
    return pc;
  }

  /** sendDtmf routes a keypress to the room's IVR (data-channel DTMF, not RFC 4733). */
  sendDtmf(digit: string): void {
    this.send({ type: ClientMsgType.DTMF, digit });
  }

  /** mute/unmute the outgoing mic. */
  setMuted(muted: boolean): void {
    this.mic?.getAudioTracks().forEach((t) => (t.enabled = !muted));
  }

  /** hold puts the other party on hold music (the brain injects MOH). The agent
   *  is muted locally so the held party hears only the music. */
  hold(): void {
    this.setMuted(true);
    this.send({ type: ClientMsgType.Hold });
  }

  /** resume takes the other party off hold and unmutes the agent. */
  resume(): void {
    this.send({ type: ClientMsgType.Resume });
    this.setMuted(false);
  }

  /** transfer re-routes the call to another agent or team; when they answer, this
   *  leg is dropped by the brain (the call socket closes → "ended"). */
  transfer(target: { toSip?: string; toTeam?: string }): void {
    this.send({ type: ClientMsgType.Transfer, to_sip: target.toSip, to_team: target.toTeam });
  }

  /** Whether this leg is currently publishing a screen-share track. */
  get sharingScreen(): boolean {
    return this.screen !== null;
  }

  /** shareScreen publishes a screen/window video track (the server renegotiates,
   *  staying the sole offerer). Pass {selfBrowserSurface:"exclude"} etc. via opts. */
  async shareScreen(constraints?: DisplayMediaStreamOptions): Promise<void> {
    if (!this.pc) throw new Error("not connected");
    this.screen = await navigator.mediaDevices.getDisplayMedia(constraints ?? { video: true });
    const track = this.screen.getVideoTracks()[0];
    if (!track) throw new Error("no screen video track captured");
    track.contentHint = "detail";
    track.onended = () => void this.stopScreen();
    this.screenSender = this.pc.addTrack(track, this.screen);
    this.send({ type: ClientMsgType.Publish, kind: "video" });
  }

  stopScreen(): void {
    if (this.screen) {
      this.screen.getTracks().forEach((t) => t.stop());
      this.screen = null;
    }
    if (this.screenSender && this.pc) {
      try {
        this.pc.removeTrack(this.screenSender);
      } catch {
        /* already gone */
      }
      this.screenSender = null;
    }
    this.send({ type: ClientMsgType.Unpublish, kind: "video" });
  }

  /** hangup ends the call (no reconnect). */
  hangup(): void {
    this.closedByUser = true;
    this.send({ type: ClientMsgType.Bye });
    this.teardown();
    this.emit({ type: "ended" });
  }

  private onSocketClose(): void {
    if (this.closedByUser) return;
    // Unexpected drop → try to resume within the edge's grace window using the
    // session token; the SFU re-points media so peers don't renegotiate.
    if (this.sessionToken && this.attemptsLeft > 0) {
      this.attemptsLeft--;
      this.emit({ type: "reconnecting" });
      this.remoteDescSet = false;
      this.pendingIce = [];
      this.pc?.close();
      this.pc = null;
      setTimeout(() => this.openSocket(), 500);
      return;
    }
    this.teardown();
    this.emit({ type: "ended", reason: "disconnected" });
  }

  private teardown(): void {
    this.pc?.close();
    this.pc = null;
    this.mic?.getTracks().forEach((t) => t.stop());
    this.mic = null;
    this.stopScreenSilently();
    this.audioEls.forEach((el) => {
      el.srcObject = null;
      el.remove();
    });
    this.audioEls = [];
    if (this.ws && this.ws.readyState <= WebSocket.OPEN) this.ws.close();
    this.ws = null;
  }

  private stopScreenSilently(): void {
    this.screen?.getTracks().forEach((t) => t.stop());
    this.screen = null;
    this.screenSender = null;
  }

  private send(m: RtcMessage): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(m));
  }
}

// ── RtcAgent ─────────────────────────────────────────────────────────

export interface RtcAgentOpts {
  gatewayUrl: string;
  /** Agent display name / sip identity. */
  name: string;
  /** A static agent JWT, or getToken to mint a fresh one per (re)connect. */
  token?: string;
  getToken?: () => Promise<string>;
  iceServers?: RTCIceServer[];
}

export type RtcAgentEvent =
  | { type: "online" }
  | { type: "offline" }
  | { type: "incoming"; room: string; from?: string }
  | { type: "error"; error: Error };

export class RtcAgent extends Emitter<RtcAgentEvent> {
  private ws: WebSocket | null = null;
  private token = "";
  private mic: MediaStream | null = null;
  private incoming: { room: string; from?: string } | null = null;

  constructor(private readonly opts: RtcAgentOpts) {
    super();
  }

  /** goOnline registers the agent's presence socket and waits for `incoming`.
   *  It also captures the mic now (in the go-online gesture) so accepting a call
   *  later — including auto-answer — needs no fresh gesture. Best-effort: if the
   *  mic is denied here, accept() captures it then. */
  async goOnline(): Promise<void> {
    if (!this.mic && window.isSecureContext && navigator.mediaDevices?.getUserMedia) {
      try {
        this.mic = await navigator.mediaDevices.getUserMedia({ audio: true });
      } catch {
        /* will capture on accept instead */
      }
    }
    this.token = this.opts.getToken ? await this.opts.getToken() : this.opts.token ?? "";
    if (!this.token) throw new Error("agent token required (token or getToken)");
    const ws = new WebSocket(wsURL(this.opts.gatewayUrl, this.token));
    this.ws = ws;
    ws.onopen = () => ws.send(JSON.stringify({ type: ClientMsgType.Register }));
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data) as RtcMessage;
      if (m.type === ServerMsgType.Ready) this.emit({ type: "online" });
      else if (m.type === ServerMsgType.Incoming) {
        this.incoming = { room: m.room ?? "", from: m.from };
        this.emit({ type: "incoming", room: this.incoming.room, from: m.from });
      }
    };
    ws.onclose = () => this.emit({ type: "offline" });
    ws.onerror = () => this.emit({ type: "error", error: new Error("presence socket error") });
  }

  /** accept answers the current incoming call: it opens a new RtcCall joining the
   *  rung room and returns it (presence stays open for the next call). Call from
   *  the Accept-button gesture so the mic is granted. Returns null if no ring. */
  async accept(): Promise<RtcCall | null> {
    if (!this.incoming) return null;
    const room = this.incoming.room;
    this.incoming = null;
    const call = new RtcCall({
      gatewayUrl: this.opts.gatewayUrl,
      room,
      token: this.token,
      name: this.opts.name,
      iceServers: this.opts.iceServers,
      mic: this.mic ?? undefined, // reuse the go-online mic (gesture-safe)
    });
    await call.join();
    return call;
  }

  /** decline drops the current ring (the edge ring times out → no_answer). */
  decline(): void {
    this.incoming = null;
  }

  /** hasIncoming reports whether a call is currently ringing (for auto-answer). */
  hasIncoming(): boolean {
    return this.incoming !== null;
  }

  goOffline(): void {
    this.ws?.close();
    this.ws = null;
    this.mic?.getTracks().forEach((t) => t.stop());
    this.mic = null;
  }
}

// micError turns a getUserMedia DOMException into a friendly message.
function micError(e: unknown): Error {
  const name = (e as DOMException | undefined)?.name;
  switch (name) {
    case "NotAllowedError":
    case "SecurityError":
      return new Error("Microphone blocked — allow microphone access and try again.");
    case "NotFoundError":
    case "OverconstrainedError":
      return new Error("No microphone found on this device.");
    case "NotReadableError":
      return new Error("Your microphone is in use by another app.");
    default:
      return e instanceof Error ? e : new Error("Could not access the microphone.");
  }
}
