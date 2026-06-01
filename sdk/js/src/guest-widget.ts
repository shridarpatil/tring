// Drop-in guest call widget. Renders a "Call support" launcher and a
// full-screen call UI (calling → connected → ended) driven by TringGuest.
// The customer taps once and is in the IVR over WebRTC — no number, no app.
//
// A page can either let the widget render its own floating launcher
// (default) or wire its own button via `<button data-tring-guest-call>`.

import { TringGuest } from "./guest";
import type { GuestInitOpts } from "./types";

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

  guest.on("connecting", () => ui.setState("connecting", "Connecting…"));
  guest.on("ringing", () => {
    ui.setState("ringing", "Calling…");
    ringer.start();
  });
  guest.on("connected", () => {
    ringer.stop();
    ui.startTimer();
  });
  guest.on("ended", () => {
    ringer.stop();
    ui.end("Call ended");
  });
  guest.on("error", ({ error }) => {
    ringer.stop();
    ui.end(error.message || "Call failed");
  });

  // Wire UI controls back to the guest session.
  ui.onHangup(() => guest.hangup());
  ui.onMute(() => guest.toggleMute());

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
  const buttons = document.querySelectorAll<HTMLButtonElement>(
    "button[data-tring-guest-call], [data-tring-guest-call]",
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

// ---- Full-screen call UI ------------------------------------------------

interface GuestUI {
  root: HTMLElement;
  open(): void;
  setState(state: string, sub: string): void;
  startTimer(): void;
  end(message: string): void;
  onHangup(fn: () => void): void;
  onMute(fn: () => boolean | void): void;
}

function createGuestUI(title: string): GuestUI {
  const root = document.createElement("div");
  root.id = UI_ID;
  root.setAttribute("data-state", "idle");
  root.innerHTML = `
    <div class="tring-guest-card">
      <div class="tring-guest-avatar">${escapeHtml(title.slice(0, 1).toUpperCase())}</div>
      <div class="tring-guest-title">${escapeHtml(title)}</div>
      <div class="tring-guest-sub">Connecting…</div>
      <div class="tring-guest-actions">
        <button type="button" class="tring-guest-btn tring-guest-mute" aria-pressed="false">Mute</button>
        <button type="button" class="tring-guest-btn tring-guest-end">End</button>
      </div>
    </div>`;

  const $ = <T extends HTMLElement>(s: string) => root.querySelector<T>(s)!;
  const subEl = $(".tring-guest-sub");
  const muteBtn = $<HTMLButtonElement>(".tring-guest-mute");
  const endBtn = $<HTMLButtonElement>(".tring-guest-end");

  let tick: number | undefined;
  let startedAt = 0;

  const fmt = (ms: number) => {
    const t = Math.floor(ms / 1000);
    const m = Math.floor(t / 60);
    return `${String(m).padStart(2, "0")}:${String(t % 60).padStart(2, "0")}`;
  };

  return {
    root,
    open() {
      muteBtn.setAttribute("aria-pressed", "false");
      muteBtn.textContent = "Mute";
      subEl.textContent = "Connecting…";
      root.setAttribute("data-state", "connecting");
      root.classList.add("tring-guest-visible");
    },
    setState(state, sub) {
      root.setAttribute("data-state", state);
      subEl.textContent = sub;
    },
    startTimer() {
      root.setAttribute("data-state", "connected");
      // performance.now is monotonic and gesture-independent — fine here.
      startedAt = performance.now();
      subEl.textContent = "00:00";
      tick = window.setInterval(() => {
        subEl.textContent = fmt(performance.now() - startedAt);
      }, 500);
    },
    end(message) {
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
        muteBtn.textContent = on ? "Unmute" : "Mute";
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

    #${UI_ID} {
      position: fixed; inset: 0; z-index: 999999;
      display: none; place-items: center;
      background: rgba(8,12,22,.93); backdrop-filter: blur(8px);
      font: 16px/1.4 system-ui, sans-serif; color: #fff;
    }
    #${UI_ID}.tring-guest-visible { display: grid; }
    #${UI_ID} .tring-guest-card { text-align: center; }
    #${UI_ID} .tring-guest-avatar {
      width: 96px; height: 96px; margin: 0 auto 1rem; border-radius: 50%;
      background: #2563eb; display: grid; place-items: center; font-size: 40px;
    }
    #${UI_ID} .tring-guest-title { font-size: 22px; font-weight: 600; }
    #${UI_ID} .tring-guest-sub {
      opacity: .8; margin-top: .3rem; font-variant-numeric: tabular-nums;
    }
    #${UI_ID} .tring-guest-actions {
      display: none; gap: 1rem; margin-top: 2.2rem; justify-content: center;
    }
    #${UI_ID}[data-state="connected"] .tring-guest-actions { display: flex; }
    #${UI_ID} .tring-guest-btn {
      border: 0; color: #fff; padding: .8rem 1.6rem; border-radius: 999px;
      font: 15px system-ui, sans-serif; cursor: pointer;
    }
    #${UI_ID} .tring-guest-mute { background: #374151; }
    #${UI_ID} .tring-guest-mute[aria-pressed="true"] { background: #b45309; }
    #${UI_ID} .tring-guest-end { background: #dc2626; }
  `;
  document.head.appendChild(style);
}
