// Minimal supervisor WebRTC client for the console dashboard: joins a pinned
// room via the gateway /rtc socket (the SFU is the sole offerer — the client
// only ever answers), publishes the operator's mic (server audience-gates it
// per mode), plays the mixed room audio, and switches supervise mode live.
//
// Deliberately dependency-free (no import from sdk/js) — the console has no
// existing WebRTC client, and this one is small enough not to warrant sharing
// sdk/js/src/rtc.ts's machinery (reconnect, screenshare, DTMF, etc. don't
// apply to a supervisor leg). Wire shapes are cross-checked against
// internal/media/signaling/protocol.go (the source of truth) — see the task
// report for the field-by-field mapping.

export type SuperviseMode = "monitor" | "whisper" | "barge";

// Loosely-typed incoming frame from the /rtc socket — one flat envelope per
// internal/media/signaling/protocol.go, only the fields this client reads.
interface RtcFrame {
  type: string;
  sdp?: string;
  candidate?: RTCIceCandidateInit;
  ice_servers?: RTCIceServer[];
  reason?: string;
}

export class SupervisorSession {
  private ws?: WebSocket;
  private pc?: RTCPeerConnection;
  private mic?: MediaStream;
  private audioEl = new Audio();
  // Guards teardown+notify so it happens at most once, whether triggered by
  // the user (hangup()) or the server (ended/error frame, socket close).
  private disposed = false;

  // Set by the caller (DashboardView) to learn when a session dies for a
  // reason OTHER than the caller itself calling hangup() — e.g. the server
  // closes the socket because the call ended, or rejects the token. Never
  // called from hangup() itself (the caller already knows and will clear
  // its own state).
  onClose?: (reason?: string) => void;

  constructor(
    private gatewayURL: string,
    private room: string,
    private token: string,
    private iceServers: RTCIceServer[],
  ) {}

  // connect captures the mic, opens the /rtc socket, and joins the room the
  // token is pinned to. The server ignores the `room` field on join for a
  // supervisor (the token pins it), but we still send it — harmless, and
  // matches the shape every other /rtc client sends.
  async connect(initial: SuperviseMode = "monitor"): Promise<void> {
    this.mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    this.setMicEnabled(initial !== "monitor"); // silent in monitor (defense-in-depth; server also gates it)

    const base = this.gatewayURL.replace(/\/$/, "").replace(/^http/, "ws");
    const url = base + "/rtc?token=" + encodeURIComponent(this.token);
    this.ws = new WebSocket(url);
    this.ws.onopen = () => this.send({ type: "join", room: this.room });
    this.ws.onmessage = (ev) => this.onMessage(JSON.parse(ev.data) as RtcFrame);
    this.ws.onclose = () => this.handleRemoteClose();
  }

  private onMessage(m: RtcFrame): void {
    switch (m.type) {
      case "joined": {
        const servers = this.iceServers.length ? this.iceServers : (m.ice_servers ?? []);
        const pc = new RTCPeerConnection({ iceServers: servers });
        this.mic?.getTracks().forEach((t) => pc.addTrack(t, this.mic!));
        pc.ontrack = (e) => {
          this.audioEl.srcObject = e.streams[0] ?? new MediaStream([e.track]);
          void this.audioEl.play().catch(() => {
            /* autoplay may need a gesture; the Listen click usually satisfies it */
          });
        };
        pc.onicecandidate = (e) => {
          if (e.candidate) this.send({ type: "ice", candidate: e.candidate.toJSON() });
        };
        this.pc = pc;
        break;
      }
      case "offer":
        if (m.sdp) void this.answer(m.sdp);
        break;
      case "ice":
        if (m.candidate) void this.pc?.addIceCandidate(m.candidate);
        break;
      case "ended":
      case "error":
        this.handleRemoteClose(m.reason);
        break;
    }
  }

  private async answer(sdp: string): Promise<void> {
    if (!this.pc) return;
    await this.pc.setRemoteDescription({ type: "offer", sdp });
    const ans = await this.pc.createAnswer();
    await this.pc.setLocalDescription(ans);
    this.send({ type: "answer", sdp: ans.sdp });
  }

  // setMode switches monitor/whisper/barge: the mic only actually goes live
  // when the server's audience gate allows it (monitor is always silent
  // server-side), but we mute locally too as defense-in-depth.
  setMode(mode: SuperviseMode): void {
    this.setMicEnabled(mode !== "monitor");
    this.send({ type: "supervise-mode", mode });
  }

  private setMicEnabled(on: boolean): void {
    this.mic?.getAudioTracks().forEach((t) => (t.enabled = on));
  }

  private send(m: Record<string, unknown>): void {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(m));
  }

  // hangup is the USER-initiated stop (operator clicks Stop, or switches to
  // another call). It tells the server, tears down, and does NOT call
  // onClose — the caller initiated this and will clear its own state.
  hangup(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.send({ type: "bye" });
    this.teardown();
  }

  // handleRemoteClose handles the SERVER/unexpected side of teardown: an
  // "ended"/"error" frame, or the socket just closing on its own. It DOES
  // notify via onClose so the Vue layer can clear controls and surface the
  // reason. Guarded by `disposed` so it can't double-fire (including after
  // our own hangup()'s ws.close() triggers this same onclose handler).
  private handleRemoteClose(reason?: string): void {
    if (this.disposed) return;
    this.disposed = true;
    this.teardown();
    this.onClose?.(reason);
  }

  // teardown is the single idempotent path that stops the pc/ws/mic and
  // clears the audio element. Both hangup() and handleRemoteClose() funnel
  // through it; neither sends `bye` here (hangup sends it itself before
  // calling teardown; a remote close means the socket is already going away).
  private teardown(): void {
    this.pc?.close();
    this.pc = undefined;
    this.ws?.close();
    this.ws = undefined;
    this.mic?.getTracks().forEach((t) => t.stop());
    this.mic = undefined;
    this.audioEl.srcObject = null;
  }
}
