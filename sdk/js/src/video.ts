export type VideoSource = "camera" | "screen";
export interface LocalVideoState {
  stream: MediaStream | null;
  source: VideoSource | null;
}

// One outgoing video track per participant. Switching sources replaces the
// track; stopping releases the device and retires its transceiver completely.
export class LocalVideo {
  stream: MediaStream | null = null;
  source: VideoSource | null = null;
  private sender: RTCRtpSender | null = null;
  private generation = 0;
  private pending = false;

  constructor(
    private readonly peer: () => RTCPeerConnection | null,
    private readonly signal: (publish: boolean) => void,
    private readonly changed: (state: LocalVideoState) => void,
  ) {}

  startCamera(constraints?: MediaTrackConstraints): Promise<void> {
    return this.start("camera", () => navigator.mediaDevices.getUserMedia({
      audio: false,
      video: constraints ?? { facingMode: "user", width: { ideal: 1280 }, height: { ideal: 720 } },
    }));
  }

  shareScreen(constraints?: DisplayMediaStreamOptions): Promise<void> {
    return this.start("screen", () => navigator.mediaDevices.getDisplayMedia({ ...constraints, video: constraints?.video ?? true, audio: false }));
  }

  private async start(source: VideoSource, capture: () => Promise<MediaStream>): Promise<void> {
    const pc = this.peer();
    if (!pc || pc.connectionState === "closed") throw new Error("Call is not connected.");
    if (!window.isSecureContext || !navigator.mediaDevices) throw new Error("Camera and screen sharing require HTTPS or localhost.");
    if (this.pending) throw new Error("A video request is already in progress.");
    if (this.source === source) return;
    this.pending = true;
    const generation = ++this.generation;
    let stream: MediaStream | undefined;
    let created: RTCRtpTransceiver | undefined;
    try {
      stream = await capture(); // invoked directly from the button gesture
      const track = stream.getVideoTracks()[0];
      if (!track) throw new Error("No video track was captured.");
      if (generation !== this.generation || this.peer() !== pc) throw new Error("Video request cancelled.");
      track.contentHint = source === "camera" ? "motion" : "detail";
      if (this.sender) {
        await this.sender.replaceTrack(track);
      } else {
        // addTrack could reuse the transceiver receiving the other person's
        // video. A dedicated sender lets both sides publish simultaneously.
        created = pc.addTransceiver(track, { direction: "sendonly", streams: [stream] });
        this.sender = created.sender;
        this.signal(true);
      }
      if (generation !== this.generation || this.peer() !== pc) throw new Error("Video request cancelled.");
      this.stream?.getTracks().forEach((t) => t.stop());
      this.stream = stream;
      this.source = source;
      track.onended = () => { if (this.stream === stream) this.stop(); };
      this.changed({ stream, source });
    } catch (error) {
      stream?.getTracks().forEach((track) => track.stop());
      if (created && this.sender === created.sender) {
        created.stop();
        this.sender = null;
        try { this.signal(false); } catch { /* signaling already unavailable */ }
      }
      throw error;
    } finally {
      this.pending = false;
    }
  }

  stop(source?: VideoSource): void {
    if (source && this.source !== source) return;
    ++this.generation; // invalidates a permission prompt that outlives the call
    const wasPublishing = this.sender !== null;
    this.stream?.getTracks().forEach((track) => track.stop());
    this.stream = null;
    this.source = null;
    const pc = this.peer();
    if (pc && this.sender) {
      try {
        pc.getTransceivers().find((t) => t.sender === this.sender)?.stop();
      } catch { /* peer already closed */ }
    }
    this.sender = null;
    if (wasPublishing) this.signal(false);
    this.changed({ stream: null, source: null });
  }
}

export function videoErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    if (error.name === "NotAllowedError") return "Camera access was not allowed. You can continue with audio.";
    if (error.name === "NotFoundError") return "No camera was found.";
    if (error.name === "NotReadableError") return "The camera is unavailable or in use by another app.";
    return error.message;
  }
  return "Could not start video. You can continue with audio.";
}
