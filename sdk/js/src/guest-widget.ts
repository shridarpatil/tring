// Drop-in guest call widget. Renders a "Call support" launcher and a
// full-screen call UI (calling → connected → ended) driven by TringGuest.
// The customer taps once and is in the IVR over WebRTC — no number, no app.
//
// A page can either let the widget render its own floating launcher
// (default) or wire its own button via `<button data-tring-call>`.

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

// ---- Full-screen call UI ------------------------------------------------

interface GuestUI {
  root: HTMLElement;
  open(): void;
  setState(state: string, sub: string): void;
  startTimer(): void;
  end(message: string): void;
  onHangup(fn: () => void): void;
  onMute(fn: () => boolean | void): void;
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

  root.innerHTML = `
    <div class="tring-guest-card">
      <div class="tring-guest-top">
        <div class="tring-guest-avatar">${escapeHtml(title.slice(0, 1).toUpperCase())}</div>
        <div class="tring-guest-title">${escapeHtml(title)}</div>
        <div class="tring-guest-sub">Connecting…</div>
      </div>
      <div class="tring-guest-mid">
        <div class="tring-guest-dialed"></div>
        <div class="tring-guest-keypad">${keypadHTML}</div>
      </div>
      <div class="tring-guest-actions">
        <button type="button" class="tring-guest-btn tring-guest-mute" aria-pressed="false">
          <span class="tring-guest-ic">${micIcon}</span><span class="tring-guest-lbl">Mute</span>
        </button>
        <button type="button" class="tring-guest-btn tring-guest-end">
          <span class="tring-guest-ic">${phoneIcon}</span><span class="tring-guest-lbl">End</span>
        </button>
      </div>
    </div>`;

  const $ = <T extends HTMLElement>(s: string) => root.querySelector<T>(s)!;
  const subEl = $(".tring-guest-sub");
  const dialedEl = $(".tring-guest-dialed");
  const keypadEl = $(".tring-guest-keypad");
  const muteBtn = $<HTMLButtonElement>(".tring-guest-mute");
  const muteLbl = muteBtn.querySelector<HTMLElement>(".tring-guest-lbl")!;
  const endBtn = $<HTMLButtonElement>(".tring-guest-end");

  let tick: number | undefined;
  let startedAt = 0;
  let dialed = "";

  const fmt = (ms: number) => {
    const t = Math.floor(ms / 1000);
    const m = Math.floor(t / 60);
    return `${String(m).padStart(2, "0")}:${String(t % 60).padStart(2, "0")}`;
  };

  return {
    root,
    open() {
      muteBtn.setAttribute("aria-pressed", "false");
      muteBtn.classList.remove("is-active");
      muteLbl.textContent = "Mute";
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
        muteBtn.classList.toggle("is-active", on);
        muteLbl.textContent = on ? "Unmute" : "Mute";
      });
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

    /* Full-screen native-style call sheet. Opaque so the host page
       doesn't bleed through; safe-area insets respect the notch/home bar. */
    #${UI_ID} {
      position: fixed; inset: 0; z-index: 999999; display: none;
      background: linear-gradient(180deg, #243042 0%, #0b1220 100%);
      font: 16px/1.4 -apple-system, system-ui, sans-serif; color: #fff;
      -webkit-tap-highlight-color: transparent;
    }
    #${UI_ID}.tring-guest-visible { display: block; }
    #${UI_ID} .tring-guest-card {
      box-sizing: border-box; width: 100%; max-width: 460px; height: 100%;
      margin: 0 auto; display: flex; flex-direction: column; align-items: center;
      padding: max(3rem, env(safe-area-inset-top)) 1.5rem
               max(1.75rem, env(safe-area-inset-bottom));
    }

    /* Caller block — upper third, like an incoming/active call. */
    #${UI_ID} .tring-guest-top { margin-top: 7vh; text-align: center; }
    #${UI_ID} .tring-guest-avatar {
      width: 104px; height: 104px; margin: 0 auto 1.1rem; border-radius: 50%;
      background: linear-gradient(135deg, #3b82f6, #2563eb);
      display: grid; place-items: center; font-size: 44px; font-weight: 600;
      box-shadow: 0 8px 30px rgba(37,99,235,.35);
    }
    #${UI_ID} .tring-guest-title { font-size: 28px; font-weight: 600; }
    #${UI_ID} .tring-guest-sub {
      font-size: 17px; opacity: .6; margin-top: .35rem;
      font-variant-numeric: tabular-nums;
    }

    /* Middle — dialed digits + dialpad, fills the gap and centers. */
    #${UI_ID} .tring-guest-mid {
      flex: 1; width: 100%; display: flex; flex-direction: column;
      align-items: center; justify-content: center;
    }
    #${UI_ID} .tring-guest-dialed {
      min-height: 1.4em; margin-bottom: 1rem; font-size: 26px;
      letter-spacing: .15em; font-variant-numeric: tabular-nums;
    }
    /* Dialpad is visible by default once connected — no extra tap. */
    #${UI_ID} .tring-guest-keypad {
      display: none; grid-template-columns: repeat(3, 1fr);
      gap: 1.1rem 1.8rem; width: min(80vw, 290px);
    }
    #${UI_ID}[data-state="connected"] .tring-guest-keypad { display: grid; }
    #${UI_ID} .tring-guest-key {
      aspect-ratio: 1; width: 100%; max-width: 76px; justify-self: center;
      border: 0; border-radius: 50%; cursor: pointer; color: #fff;
      background: rgba(255,255,255,.1);
      display: flex; flex-direction: column; align-items: center;
      justify-content: center; transition: background .1s;
    }
    #${UI_ID} .tring-guest-key:active { background: rgba(255,255,255,.28); }
    #${UI_ID} .tring-guest-key .num { font-size: 29px; font-weight: 500; line-height: 1; }
    #${UI_ID} .tring-guest-key .sub {
      font-size: 9px; letter-spacing: .12em; opacity: .65;
      margin-top: 3px; min-height: 10px;
    }

    /* Bottom — circular call controls, like a native dialer. */
    #${UI_ID} .tring-guest-actions {
      display: none; width: 100%; justify-content: center; gap: 3.5rem;
      padding-top: 1.5rem;
    }
    #${UI_ID}[data-state="connected"] .tring-guest-actions { display: flex; }
    #${UI_ID} .tring-guest-btn {
      background: none; border: 0; padding: 0; cursor: pointer; color: #fff;
      display: flex; flex-direction: column; align-items: center; gap: .55rem;
      font: 13px -apple-system, system-ui, sans-serif;
    }
    #${UI_ID} .tring-guest-ic {
      width: 66px; height: 66px; border-radius: 50%;
      display: grid; place-items: center;
    }
    #${UI_ID} .tring-guest-mute .tring-guest-ic { background: rgba(255,255,255,.15); }
    #${UI_ID} .tring-guest-mute.is-active .tring-guest-ic { background: #fff; color: #111827; }
    #${UI_ID} .tring-guest-end .tring-guest-ic { background: #ef4444; }
    /* Rotate the phone glyph so End reads as "hang up". */
    #${UI_ID} .tring-guest-end .tring-guest-ic svg { transform: rotate(135deg); }
  `;
  document.head.appendChild(style);
}
