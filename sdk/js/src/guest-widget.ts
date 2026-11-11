// Drop-in guest call widget. Renders a "Call support" launcher and a
// floating call UI (calling → connected → ended) driven by TringGuest.
// The customer taps once and is in the IVR over WebRTC — no number, no app.
//
// A page can either let the widget render its own floating launcher
// (default) or wire its own button via `<button data-tring-call>`.

import { TringGuest } from "./guest";
import type { GuestInitOpts } from "./types";
import { videoErrorMessage, type VideoSource } from "./video";

const UI_ID = "tring-guest-ui";

export interface GuestWidgetOpts extends GuestInitOpts {
  /** Render the default floating launcher button. Default: true. */
  renderButton?: boolean;
  /** Launcher label. Default: "Call support". */
  buttonLabel?: string;
  /** Heading shown on the call screen. Default: "Support". */
  title?: string;
}

export function bootstrapGuest(opts: GuestWidgetOpts): TringGuest {
  injectStyles();
  const guest = new TringGuest(opts);
  const ui = createGuestUI(opts.title ?? "Support");
  document.body.appendChild(ui.root);

  const ringer = new Ringer();
  let answered = false;

  guest.on("connecting", () => {
    answered = false;
    ui.setState("connecting", "Connecting…");
  });
  guest.on("ringing", () => {
    ui.setState("ringing", "Calling…");
    ringer.start();
  });
  guest.on("connected", () => {
    // The gateway/IVR is reachable; it does not mean an agent has picked up.
    // Stop local ringback so it cannot mask IVR prompts or server hold music.
    ringer.stop();
    if (!answered) ui.setState("connected", "Calling…");
  });
  guest.on("answered", () => {
    answered = true;
    ringer.stop();
    ui.startTimer();
  });
  guest.on("ended", () => {
    ringer.stop();
    ui.setRemoteVideo(null);
    ui.end("Call ended");
  });
  guest.on("error", ({ error }) => {
    ringer.stop();
    ui.setRemoteVideo(null);
    ui.end(error.message || "Call failed");
  });
  guest.on("local-video", ({ stream, source }) => ui.setLocalVideo(stream, source));
  // The agent's camera or screen arrives as a video track.
  guest.on("remote-video", ({ stream }) => ui.setRemoteVideo(stream));

  // Wire UI controls back to the guest session.
  ui.onHangup(() => guest.hangup());
  ui.onMute(() => guest.toggleMute());
  ui.onShare(() => guest.toggleScreen());
  ui.onCamera(() => guest.toggleCamera());

  ui.onDigit((digit) => guest.sendDTMF(digit));

  const startCall = async () => {
    if (guest.inCall) return;
    ui.open();
    try {
      await guest.call();
    } catch (e) {
      // error event already handled the UI; just log.
      console.error("[tring-guest] call failed:", e);
    }
  };

  bindButtons(startCall);
  if (opts.renderButton !== false) {
    document.body.appendChild(
      createLauncher(opts.buttonLabel ?? "Call support", startCall),
    );
  }

  // Re-bind dynamically-added buttons (SPA-rendered support pages).
  const mo = new MutationObserver(() => bindButtons(startCall));
  mo.observe(document.body, { childList: true, subtree: true });

  return guest;
}

const BOUND_MARK = "__tringGuestBound";

function bindButtons(onClick: () => void): void {
  // Put `data-tring-call` on any element to wire it to the call instead of the
  // floating launcher (pair with data-render-button="false" to drop the
  // launcher). Visitor-only — the agent widget binds no buttons (it can't
  // originate; calls are placed by the backend).
  const buttons = document.querySelectorAll<HTMLButtonElement>(
    "[data-tring-call]",
  );
  for (const btn of Array.from(buttons)) {
    if ((btn as any)[BOUND_MARK]) continue;
    (btn as any)[BOUND_MARK] = true;
    btn.addEventListener("click", (ev) => {
      ev.preventDefault();
      onClick();
    });
  }
}

function createLauncher(label: string, onClick: () => void): HTMLButtonElement {
  const b = document.createElement("button");
  b.className = "tring-guest-launcher";
  b.type = "button";
  b.innerHTML = `<span class="tring-guest-launcher-icon">📞</span>${escapeHtml(label)}`;
  b.addEventListener("click", onClick);
  return b;
}

// ---- Floating call UI ------------------------------------------------

interface GuestUI {
  root: HTMLElement;
  open(): void;
  setState(state: string, sub: string): void;
  startTimer(): void;
  end(message: string): void;
  onHangup(fn: () => void): void;
  onMute(fn: () => boolean | void): void;
  onShare(fn: () => Promise<boolean> | boolean | void): void;
  onCamera(fn: () => Promise<boolean> | boolean | void): void;
  setLocalVideo(stream: MediaStream | null, source: VideoSource | null): void;
  setRemoteVideo(stream: MediaStream | null): void;
  onDigit(fn: (digit: string) => void): void;
}

function createGuestUI(title: string): GuestUI {
  const root = document.createElement("div");
  root.id = UI_ID;
  root.setAttribute("data-state", "idle");
  const DTMF_KEYS = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];
  const KEY_SUB: Record<string, string> = {
    "2": "ABC", "3": "DEF", "4": "GHI", "5": "JKL", "6": "MNO",
    "7": "PQRS", "8": "TUV", "9": "WXYZ", "0": "+",
  };
  const keypadHTML = DTMF_KEYS.map(
    (k) =>
      `<button type="button" class="tring-guest-key" data-d="${k}">` +
      `<span class="num">${k}</span><span class="sub">${KEY_SUB[k] ?? ""}</span></button>`,
  ).join("");

  // Inline SVGs so the controls look native with no asset dependency.
  const micIcon = `<svg viewBox="0 0 24 24" width="26" height="26" fill="currentColor"><path d="M12 14a3 3 0 0 0 3-3V5a3 3 0 0 0-6 0v6a3 3 0 0 0 3 3zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.92V21h2v-3.08A7 7 0 0 0 19 11h-2z"/></svg>`;
  const phoneIcon = `<svg viewBox="0 0 24 24" width="28" height="28" fill="currentColor"><path d="M6.6 10.8c1.4 2.8 3.8 5.1 6.6 6.6l2.2-2.2c.3-.3.7-.4 1-.2 1.1.4 2.3.6 3.6.6.6 0 1 .4 1 1V20c0 .6-.4 1-1 1C10.6 21 3 13.4 3 4c0-.6.4-1 1-1h3.5c.6 0 1 .4 1 1 0 1.2.2 2.4.6 3.6.1.4 0 .8-.3 1l-2.2 2.2z"/></svg>`;
  const cameraIcon = `<svg viewBox="0 0 24 24" width="26" height="26" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><rect x="3" y="5" width="12" height="14" rx="2"/><path d="m15 10 6-4v12l-6-4z"/></svg>`;
  const keypadIcon = `<svg viewBox="0 0 24 24" width="26" height="26" fill="currentColor"><path d="M4 3h4v4H4zm6 0h4v4h-4zm6 0h4v4h-4zM4 9h4v4H4zm6 0h4v4h-4zm6 0h4v4h-4zM4 15h4v4H4zm6 0h4v4h-4zm6 0h4v4h-4z"/></svg>`;
  const screenIcon = `<svg viewBox="0 0 24 24" width="26" height="26" fill="currentColor"><path d="M20 4H4a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h6v2H8v2h8v-2h-2v-2h6a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2zm0 12H4V6h16v10z"/></svg>`;

  root.innerHTML = `
    <div class="tring-guest-card">
      <div class="tring-guest-top">
        <div class="tring-guest-avatar">${escapeHtml(title.slice(0, 1).toUpperCase())}</div>
        <div class="tring-guest-heading"><div class="tring-guest-title">${escapeHtml(title)}</div>
          <div class="tring-guest-sub">Connecting…</div></div>
        <button type="button" class="tring-guest-minimize" aria-label="Minimize call" aria-expanded="true">−</button>
      </div>
      <div class="tring-guest-mid">
        <div class="tring-guest-video">
          <div class="tring-guest-pictures">
            <video class="tring-guest-remote" autoplay playsinline muted aria-label="Agent video"></video>
            <video class="tring-guest-local" autoplay playsinline muted aria-label="Your camera preview"></video>
          </div>
          <div class="tring-guest-video-bar"><span>Live video</span><button type="button" class="tring-guest-fullscreen">Full screen</button></div>
        </div>
        <div class="tring-guest-dialed"></div>
        <div class="tring-guest-keypad">${keypadHTML}</div>
      </div>
      <div class="tring-guest-actions">
        <button type="button" class="tring-guest-btn tring-guest-mute" aria-pressed="false">
          <span class="tring-guest-ic">${micIcon}</span><span class="tring-guest-lbl">Mute</span>
        </button>
        <button type="button" class="tring-guest-btn tring-guest-keypad-toggle" aria-label="Keypad" aria-expanded="false" aria-pressed="false">
          <span class="tring-guest-ic">${keypadIcon}</span><span class="tring-guest-lbl">Keypad</span>
        </button>
        <button type="button" class="tring-guest-btn tring-guest-camera" aria-pressed="false" aria-label="Turn camera on">
          <span class="tring-guest-ic">${cameraIcon}</span><span class="tring-guest-lbl">Camera</span>
        </button>
        <button type="button" class="tring-guest-btn tring-guest-share" aria-pressed="false">
          <span class="tring-guest-ic">${screenIcon}</span><span class="tring-guest-lbl">Share</span>
        </button>
        <button type="button" class="tring-guest-btn tring-guest-end">
          <span class="tring-guest-ic">${phoneIcon}</span><span class="tring-guest-lbl">End</span>
        </button>
      </div>
      <div class="tring-guest-video-error" role="status"></div>
    </div>`;

  const $ = <T extends HTMLElement>(s: string) => root.querySelector<T>(s)!;
  const subEl = $(".tring-guest-sub");
  const dialedEl = $(".tring-guest-dialed");
  const keypadEl = $(".tring-guest-keypad");
  const keypadBtn = $<HTMLButtonElement>(".tring-guest-keypad-toggle");
  function setKeypad(open: boolean) {
    root.classList.toggle("tring-guest-keypad-open", open);
    keypadBtn.classList.toggle("is-active", open);
    keypadBtn.setAttribute("aria-expanded", String(open));
    keypadBtn.setAttribute("aria-pressed", String(open));
  }
  keypadBtn.addEventListener("click", () => {
    if (root.getAttribute("data-state") !== "connected") return;
    setKeypad(!root.classList.contains("tring-guest-keypad-open"));
  });
  const muteBtn = $<HTMLButtonElement>(".tring-guest-mute");
  const muteLbl = muteBtn.querySelector<HTMLElement>(".tring-guest-lbl")!;
  const shareBtn = $<HTMLButtonElement>(".tring-guest-share");
  const shareLbl = shareBtn.querySelector<HTMLElement>(".tring-guest-lbl")!;
  const remoteVid = $<HTMLVideoElement>(".tring-guest-remote");
  const localVid = $<HTMLVideoElement>(".tring-guest-local");
  const cameraBtn = $<HTMLButtonElement>(".tring-guest-camera");
  const cameraLbl = cameraBtn.querySelector<HTMLElement>(".tring-guest-lbl")!;
  const videoError = $(".tring-guest-video-error");
  const videoStage = $(".tring-guest-video");
  const fullscreenBtn = $<HTMLButtonElement>(".tring-guest-fullscreen");
  const minimizeBtn = $<HTMLButtonElement>(".tring-guest-minimize");
  function setMinimized(minimized: boolean) {
    root.classList.toggle("tring-guest-min", minimized);
    minimizeBtn.textContent = minimized ? "+" : "−";
    minimizeBtn.setAttribute("aria-label", minimized ? "Expand call" : "Minimize call");
    minimizeBtn.setAttribute("aria-expanded", String(!minimized));
  }
  minimizeBtn.addEventListener("click", () => setMinimized(!root.classList.contains("tring-guest-min")));
  fullscreenBtn.addEventListener("click", async () => {
    try {
      if (document.fullscreenElement === videoStage) await document.exitFullscreen();
      else if (videoStage.requestFullscreen) await videoStage.requestFullscreen();
      else {
        const video = (remoteVid.srcObject ? remoteVid : localVid) as HTMLVideoElement & { webkitEnterFullscreen?: () => void };
        if (video.webkitEnterFullscreen) video.webkitEnterFullscreen();
        else throw new Error("Full-screen video is unavailable in this browser.");
      }
    } catch (error) { videoError.textContent = videoErrorMessage(error); }
  });
  document.addEventListener("fullscreenchange", () => {
    fullscreenBtn.textContent = document.fullscreenElement === videoStage ? "Exit full screen" : "Full screen";
  });
  function exitVideoFullscreen() {
    if (document.fullscreenElement === videoStage) void document.exitFullscreen().catch(() => {});
  }
  let generation = 0;
  const endBtn = $<HTMLButtonElement>(".tring-guest-end");

  let tick: number | undefined;
  let startedAt = 0;
  let dialed = "";

  const fmt = (ms: number) => {
    const t = Math.floor(ms / 1000);
    const m = Math.floor(t / 60);
    return `${String(m).padStart(2, "0")}:${String(t % 60).padStart(2, "0")}`;
  };

  function setLocalVideo(stream: MediaStream | null, source: VideoSource | null) {
    const camera = source === "camera";
    const screen = source === "screen";
    localVid.srcObject = camera ? stream : null;
    if (!camera && !remoteVid.srcObject) exitVideoFullscreen();
    root.classList.toggle("tring-guest-camera-on", camera);
    cameraBtn.setAttribute("aria-pressed", String(camera));
    cameraBtn.classList.toggle("is-active", camera);
    cameraBtn.setAttribute("aria-label", camera ? "Turn camera off" : "Turn camera on");
    cameraLbl.textContent = camera ? "Camera off" : "Camera";
    shareBtn.setAttribute("aria-pressed", String(screen));
    shareBtn.classList.toggle("is-active", screen);
    shareLbl.textContent = screen ? "Stop share" : "Share";
  }
  function bindVideo(button: HTMLButtonElement, fn: () => Promise<boolean> | boolean | void) {
    button.addEventListener("click", async () => {
      const current = generation;
      cameraBtn.disabled = shareBtn.disabled = true;
      videoError.textContent = "";
      try { await fn(); }
      catch (error) { if (current === generation) videoError.textContent = videoErrorMessage(error); }
      finally { if (current === generation) cameraBtn.disabled = shareBtn.disabled = false; }
    });
  }

  return {
    root,
    open() {
      ++generation;
      if (tick !== undefined) clearInterval(tick);
      tick = undefined;
      setMinimized(false);
      setKeypad(false);
      exitVideoFullscreen();
      setLocalVideo(null, null);
      cameraBtn.disabled = false;
      videoError.textContent = "";
      muteBtn.setAttribute("aria-pressed", "false");
      muteBtn.classList.remove("is-active");
      muteLbl.textContent = "Mute";
      shareBtn.setAttribute("aria-pressed", "false");
      shareBtn.classList.remove("is-active");
      shareBtn.disabled = false;
      shareLbl.textContent = "Share";
      remoteVid.srcObject = null;
      root.classList.remove("tring-guest-has-video");
      dialed = "";
      dialedEl.textContent = "";
      subEl.textContent = "Connecting…";
      root.setAttribute("data-state", "connecting");
      root.classList.add("tring-guest-visible");
    },
    setState(state, sub) {
      root.setAttribute("data-state", state);
      subEl.textContent = sub;
    },
    startTimer() {
      if (tick !== undefined) return;
      root.setAttribute("data-state", "connected");
      // performance.now is monotonic and gesture-independent — fine here.
      startedAt = performance.now();
      subEl.textContent = "00:00";
      tick = window.setInterval(() => {
        subEl.textContent = fmt(performance.now() - startedAt);
      }, 500);
    },
    end(message) {
      ++generation;
      setMinimized(false);
      setKeypad(false);
      exitVideoFullscreen();
      setLocalVideo(null, null);
      videoError.textContent = "";
      if (tick) {
        clearInterval(tick);
        tick = undefined;
      }
      root.setAttribute("data-state", "ended");
      subEl.textContent = message;
      window.setTimeout(() => root.classList.remove("tring-guest-visible"), 1400);
    },
    onHangup(fn) {
      endBtn.addEventListener("click", () => fn());
    },
    onMute(fn) {
      muteBtn.addEventListener("click", () => {
        const muted = fn();
        const on = muted === true;
        muteBtn.setAttribute("aria-pressed", String(on));
        muteBtn.classList.toggle("is-active", on);
        muteLbl.textContent = on ? "Unmute" : "Mute";
      });
    },
    onShare(fn) { bindVideo(shareBtn, fn); },
    onCamera(fn) { bindVideo(cameraBtn, fn); },
    setLocalVideo,
    setRemoteVideo(stream) {
      if (stream) {
        remoteVid.srcObject = stream;
        root.classList.add("tring-guest-has-video");
      } else {
        remoteVid.srcObject = null;
        root.classList.remove("tring-guest-has-video");
        if (!localVid.srcObject) exitVideoFullscreen();
      }
    },
    onDigit(fn) {
      // One delegated listener covers all 12 keys.
      keypadEl.addEventListener("click", (ev) => {
        const btn = (ev.target as HTMLElement).closest<HTMLButtonElement>(
          "button.tring-guest-key",
        );
        if (!btn) return;
        const digit = btn.dataset.d!;
        dialed += digit;
        dialedEl.textContent = dialed;
        fn(digit);
      });
    },
  };
}

// ---- Ringback tone (WebAudio — no asset to ship) ------------------------

class Ringer {
  private ctx: AudioContext | null = null;
  private timer: number | undefined;

  start(): void {
    // AudioContext may start suspended until a user gesture; the call is
    // initiated by a click, so resume() succeeds here.
    try {
      this.ctx = new AudioContext();
      void this.ctx.resume();
    } catch {
      return;
    }
    const beep = () => {
      if (!this.ctx) return;
      const o = this.ctx.createOscillator();
      const g = this.ctx.createGain();
      o.frequency.value = 440;
      g.gain.value = 0.12;
      o.connect(g);
      g.connect(this.ctx.destination);
      o.start();
      o.stop(this.ctx.currentTime + 0.4);
    };
    beep();
    // UK-style ringback cadence: short tone, long-ish gap.
    this.timer = window.setInterval(beep, 2400);
  }

  stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
    void this.ctx?.close().catch(() => {});
    this.ctx = null;
  }
}

// ---- helpers ------------------------------------------------------------

function escapeHtml(s: string): string {
  return s.replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ]!,
  );
}

function injectStyles(): void {
  if (document.getElementById("tring-guest-styles")) return;
  const style = document.createElement("style");
  style.id = "tring-guest-styles";
  style.textContent = `
    .tring-guest-launcher {
      position: fixed; right: 1.5rem; bottom: 1.5rem; z-index: 999998;
      display: inline-flex; align-items: center; gap: .5rem;
      padding: .8rem 1.3rem; border: 0; border-radius: 999px;
      background: #2563eb; color: #fff; cursor: pointer;
      font: 600 15px/1 system-ui, sans-serif;
      box-shadow: 0 10px 30px rgba(37,99,235,.4);
    }
    .tring-guest-launcher:hover { background: #1d4ed8; }
    .tring-guest-launcher-icon { font-size: 18px; }

    /* Compact call card, matching the agent widget and leaving the host page usable. */
    #${UI_ID} {
      position: fixed; right: max(1rem, env(safe-area-inset-right)); bottom: max(1rem, env(safe-area-inset-bottom));
      width: 400px; max-width: calc(100vw - 2rem); z-index: 999999; display: none;
      color: #1f2420; font: 14px/1.4 system-ui, -apple-system, "Segoe UI", sans-serif;
      -webkit-tap-highlight-color: transparent;
    }
    #${UI_ID} * { box-sizing: border-box; }
    #${UI_ID}.tring-guest-visible { display: block; }
    #${UI_ID} button { font: inherit; cursor: pointer; }
    #${UI_ID} button:focus-visible { outline: 2px solid #0f5c46; outline-offset: 2px; }
    #${UI_ID} .tring-guest-card {
      width: 100%; max-height: calc(100dvh - 2rem); overflow-y: auto;
      background: #fbfaf7; border: 1px solid #e4e0d5; border-radius: 22px;
      box-shadow: 0 18px 40px rgba(31,36,32,.12), 0 2px 6px rgba(31,36,32,.06);
      padding: 20px; display: flex; flex-direction: column; gap: 16px;
    }
    #${UI_ID} .tring-guest-top { display: flex; align-items: center; gap: 14px; }
    #${UI_ID} .tring-guest-avatar {
      width: 52px; height: 52px; border-radius: 14px; flex: none;
      background: #0f5c46; color: #fff; display: grid; place-items: center;
      font: 600 24px/1 Georgia, "Times New Roman", serif;
    }
    #${UI_ID} .tring-guest-heading { flex: 1; min-width: 0; }
    #${UI_ID} .tring-guest-title { font-size: 16px; font-weight: 700; overflow-wrap: anywhere; }
    #${UI_ID} .tring-guest-sub { color: #5c6058; font-size: 12px; font-variant-numeric: tabular-nums; margin-top: 4px; }
    #${UI_ID} .tring-guest-minimize { border: 0; border-radius: 8px; background: transparent; color: #5c6058; width: 32px; height: 32px; font-size: 22px; }
    #${UI_ID}.tring-guest-min { width: 280px; }
    #${UI_ID}.tring-guest-min .tring-guest-card { padding: 12px 16px; }
    #${UI_ID}.tring-guest-min .tring-guest-mid,
    #${UI_ID}.tring-guest-min .tring-guest-actions,
    #${UI_ID}.tring-guest-min .tring-guest-video-error { display: none; }
    #${UI_ID} .tring-guest-mid { width: 100%; }
    #${UI_ID} .tring-guest-video { display: none; padding: 8px; background: #f6f3ea; border-radius: 14px; }
    #${UI_ID}.tring-guest-camera-on .tring-guest-video,
    #${UI_ID}.tring-guest-has-video .tring-guest-video { display: block; }
    #${UI_ID} .tring-guest-pictures { position: relative; background: #18241f; border-radius: 10px; overflow: hidden; }
    #${UI_ID} .tring-guest-remote,
    #${UI_ID} .tring-guest-local { display: none; width: 100%; height: 220px; object-fit: contain; background: #18241f; }
    #${UI_ID}.tring-guest-has-video .tring-guest-remote { display: block; }
    #${UI_ID}.tring-guest-camera-on .tring-guest-local { display: block; transform: scaleX(-1); }
    #${UI_ID}.tring-guest-has-video.tring-guest-camera-on .tring-guest-local {
      position: absolute; right: 8px; bottom: 8px; width: 96px; height: 72px; object-fit: cover;
      border: 2px solid #fbfaf7; border-radius: 8px;
    }
    #${UI_ID} .tring-guest-video-bar { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 4px 0; font-size: 12px; }
    #${UI_ID} .tring-guest-fullscreen { padding: 6px 10px; color: #1f2420; background: #fbfaf7; border: 1px solid #dcd8cc; border-radius: 8px; }
    #${UI_ID} .tring-guest-video:fullscreen { display: flex; flex-direction: column; width: 100%; height: 100%; background: #111; border-radius: 0; padding: 12px; }
    #${UI_ID} .tring-guest-video:fullscreen .tring-guest-pictures { flex: 1; min-height: 0; background: #111; }
    #${UI_ID} .tring-guest-video:fullscreen .tring-guest-remote,
    #${UI_ID}:not(.tring-guest-has-video) .tring-guest-video:fullscreen .tring-guest-local { height: 100%; background: #111; }
    #${UI_ID}.tring-guest-has-video .tring-guest-video:fullscreen .tring-guest-local { width: min(24vw, 240px); height: auto; aspect-ratio: 4/3; }
    #${UI_ID} .tring-guest-video:fullscreen .tring-guest-video-bar { color: #fff; }
    #${UI_ID} .tring-guest-dialed { min-height: 0; margin-bottom: 8px; text-align: center; font: 17px/1.4 monospace; letter-spacing: .15em; }
    #${UI_ID} .tring-guest-dialed:empty { display: none; }
    #${UI_ID} .tring-guest-keypad { display: none; grid-template-columns: repeat(3, 1fr); gap: 6px; padding: 10px; background: #f6f3ea; border-radius: 14px; }
    #${UI_ID}[data-state="connected"].tring-guest-keypad-open .tring-guest-keypad { display: grid; }
    #${UI_ID}.tring-guest-keypad-open .tring-guest-video { display: none; }
    #${UI_ID}:not(.tring-guest-keypad-open) .tring-guest-dialed { display: none; }
    #${UI_ID} .tring-guest-key { min-height: 42px; border: 0; border-radius: 8px; background: #fbfaf7; color: #1f2420; display: flex; align-items: center; justify-content: center; gap: 4px; }
    #${UI_ID} .tring-guest-key .num { font-size: 18px; }
    #${UI_ID} .tring-guest-key .sub { font-size: 8px; color: #5c6058; }
    #${UI_ID} .tring-guest-actions { display: flex; gap: 8px; }
    #${UI_ID} .tring-guest-btn { flex: 1; min-width: 0; border: 0; padding: 8px 0; border-radius: 12px; background: #ece9e0; color: #1f2420; display: flex; flex-direction: column; align-items: center; gap: 4px; font-size: 11px; }
    #${UI_ID} .tring-guest-btn:not(.tring-guest-end) { display: none; }
    #${UI_ID}[data-state="connected"] .tring-guest-btn { display: flex; }
    #${UI_ID}[data-state="ended"] .tring-guest-actions { display: none; }
    #${UI_ID} .tring-guest-btn.is-active { background: #0f5c46; color: #fff; }
    #${UI_ID} .tring-guest-btn:disabled { opacity: .5; cursor: default; }
    #${UI_ID} .tring-guest-ic { display: grid; place-items: center; height: 26px; }
    #${UI_ID} .tring-guest-ic svg { width: 22px; height: 22px; }
    #${UI_ID} .tring-guest-end { background: #c93627; color: #fff; }
    #${UI_ID} .tring-guest-end .tring-guest-ic svg { transform: rotate(135deg); }
    #${UI_ID} .tring-guest-video-error { color: #a3271b; font-size: 12px; }
    #${UI_ID} .tring-guest-video-error:empty { display: none; }
  `;
  document.head.appendChild(style);
}
