// Auto-bound widget. Imports the Tring SDK, scans the DOM for
// `<button data-tring-call data-to="...">`, attaches click handlers, and
// renders a small floating call panel (ringing / connected status +
// hangup button) when a call is in flight.
//
// Designed so a CRM can drop a single <script> tag in their template and
// get working voice calls without writing JS. See widget-entry.ts for the
// data-attribute reader.

import { Tring } from "./tring";
import type { Call } from "./types";

export interface WidgetOpts {
  /** JWT — preferred for short-lived static tokens. */
  token?: string;
  /** URL to GET a fresh JWT from. Server replies with `{token: "..."}`. */
  tokenUrl?: string;
  /** Tring API base, e.g. https://api.tring.io. */
  apiBase: string;
}

const PANEL_ID = "tring-call-panel";

export async function bootstrap(opts: WidgetOpts): Promise<Tring> {
  const token = opts.token ?? (await fetchTokenFromUrl(opts.tokenUrl));
  if (!token) {
    throw new Error(
      "tring widget: need either data-token or data-token-url on the <script> tag",
    );
  }

  injectStyles();
  const panel = createPanel();
  document.body.appendChild(panel.root);

  const tring = await Tring.init({ token, apiBase: opts.apiBase });

  tring.on("registered", () => panel.setStatus("idle", "ready"));
  tring.on("unregistered", () => panel.setStatus("idle", "disconnected"));
  tring.on("call:ringing", ({ call }) => panel.show(call, "ringing"));
  tring.on("call:answered", ({ call }) => panel.setActive(call, "connected"));
  tring.on("call:ended", () => panel.hide());
  tring.on("error", ({ error }) => panel.setStatus("error", error.message));

  bindButtons(tring);

  // Re-bind on DOM mutations so dynamically-rendered CRM rows pick up
  // the click handler. Idempotent — already-bound buttons are skipped.
  const mo = new MutationObserver(() => bindButtons(tring));
  mo.observe(document.body, { childList: true, subtree: true });

  return tring;
}

async function fetchTokenFromUrl(url?: string): Promise<string | null> {
  if (!url) return null;
  const r = await fetch(url, { credentials: "same-origin" });
  if (!r.ok) {
    throw new Error(
      `tring widget: token endpoint ${url} returned ${r.status}`,
    );
  }
  const body = await r.json();
  return body.token ?? null;
}

const BOUND_MARK = "__tringBound";

function bindButtons(tring: Tring): void {
  const buttons = document.querySelectorAll<HTMLButtonElement>(
    "button[data-tring-call]",
  );
  for (const btn of Array.from(buttons)) {
    if ((btn as any)[BOUND_MARK]) continue;
    (btn as any)[BOUND_MARK] = true;
    btn.addEventListener("click", async (ev) => {
      ev.preventDefault();
      const to = resolveDestination(btn);
      if (!to) {
        console.warn("[tring] button has no data-to or data-to-target", btn);
        return;
      }
      try {
        btn.disabled = true;
        await tring.call(to);
      } catch (e) {
        console.error("[tring] call failed:", e);
      } finally {
        btn.disabled = false;
      }
    });
  }
}

function resolveDestination(btn: HTMLButtonElement): string {
  const direct = btn.getAttribute("data-to");
  if (direct) return direct.trim();
  const target = btn.getAttribute("data-to-target");
  if (!target) return "";
  const el = document.querySelector(target);
  if (!el) return "";
  if (el instanceof HTMLInputElement) return el.value.trim();
  return (el.textContent ?? "").trim();
}

// ---- Panel ----------------------------------------------------------

interface Panel {
  root: HTMLElement;
  show(call: Call, status: string): void;
  setActive(call: Call, status: string): void;
  setStatus(state: string, text: string): void;
  hide(): void;
}

function createPanel(): Panel {
  const root = document.createElement("div");
  root.id = PANEL_ID;
  root.setAttribute("data-state", "idle");
  // Dialpad keys laid out as a real phone (1-2-3, 4-5-6, 7-8-9, *-0-#).
  // Rendered as a 3-col grid so the visual matches what users tap on
  // their phone. Only shown when the call is connected — sending DTMF
  // before answer / after hangup is a no-op the SDK would silently
  // swallow anyway.
  const DTMF_KEYS = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];
  const dtmfButtonsHTML = DTMF_KEYS.map(
    (k) => `<button type="button" class="tring-dtmf" data-d="${k}">${k}</button>`,
  ).join("");

  root.innerHTML = `
    <div class="tring-panel-row tring-panel-header">
      <span class="tring-panel-icon">📞</span>
      <span class="tring-panel-to"></span>
    </div>
    <div class="tring-panel-row tring-panel-status">Connecting…</div>
    <div class="tring-panel-row tring-panel-dialed"></div>
    <div class="tring-panel-row tring-panel-dialpad">${dtmfButtonsHTML}</div>
    <div class="tring-panel-row tring-panel-actions">
      <button type="button" class="tring-hangup">Hang up</button>
    </div>
  `;

  const toEl = root.querySelector<HTMLElement>(".tring-panel-to")!;
  const statusEl = root.querySelector<HTMLElement>(".tring-panel-status")!;
  const dialedEl = root.querySelector<HTMLElement>(".tring-panel-dialed")!;
  const hangupBtn = root.querySelector<HTMLButtonElement>(".tring-hangup")!;
  const dialpadEl = root.querySelector<HTMLElement>(".tring-panel-dialpad")!;

  let active: Call | null = null;
  let dialed = "";

  hangupBtn.addEventListener("click", async () => {
    if (active) await active.hangup();
  });

  // Event-delegate the dialpad so a single listener covers all 12 keys.
  dialpadEl.addEventListener("click", async (ev) => {
    const btn = (ev.target as HTMLElement).closest<HTMLButtonElement>(
      "button.tring-dtmf",
    );
    if (!btn || !active) return;
    const digit = btn.dataset.d!;
    dialed += digit;
    dialedEl.textContent = dialed;
    try {
      await active.sendDTMF(digit);
    } catch (e) {
      console.warn("[tring] sendDTMF failed", e);
    }
  });

  return {
    root,
    show(call, status) {
      active = call;
      dialed = "";
      dialedEl.textContent = "";
      toEl.textContent = call.to;
      statusEl.textContent = status;
      root.setAttribute("data-state", status);
      root.classList.add("tring-panel-visible");
    },
    setActive(call, status) {
      active = call;
      statusEl.textContent = status;
      root.setAttribute("data-state", status);
    },
    setStatus(state, text) {
      statusEl.textContent = text;
      root.setAttribute("data-state", state);
    },
    hide() {
      active = null;
      dialed = "";
      dialedEl.textContent = "";
      root.classList.remove("tring-panel-visible");
    },
  };
}

function injectStyles(): void {
  if (document.getElementById("tring-styles")) return;
  const style = document.createElement("style");
  style.id = "tring-styles";
  style.textContent = `
    #${PANEL_ID} {
      position: fixed; right: 1.5rem; bottom: 1.5rem;
      width: 280px; padding: 1rem 1.25rem;
      background: #111827; color: white; border-radius: 12px;
      box-shadow: 0 10px 40px rgba(0,0,0,.35);
      font: 14px/1.4 system-ui, sans-serif;
      transform: translateY(180%); opacity: 0;
      transition: transform .2s ease, opacity .2s ease;
      z-index: 999999;
    }
    #${PANEL_ID}.tring-panel-visible {
      transform: translateY(0); opacity: 1;
    }
    #${PANEL_ID} .tring-panel-row { margin-bottom: .5rem; }
    #${PANEL_ID} .tring-panel-row:last-child { margin-bottom: 0; }
    #${PANEL_ID} .tring-panel-header {
      display: flex; align-items: center; gap: .5rem; font-weight: 600;
    }
    #${PANEL_ID} .tring-panel-icon { font-size: 18px; }
    #${PANEL_ID} .tring-panel-status {
      color: #9ca3af; font-size: 12px;
    }
    #${PANEL_ID}[data-state="ringing"] .tring-panel-status { color: #fbbf24; }
    #${PANEL_ID}[data-state="connected"] .tring-panel-status { color: #34d399; }
    #${PANEL_ID}[data-state="error"] .tring-panel-status { color: #f87171; }
    #${PANEL_ID} .tring-hangup {
      background: #dc2626; color: white; border: none;
      padding: .5rem .75rem; border-radius: 6px;
      font: inherit; cursor: pointer; width: 100%;
    }
    #${PANEL_ID} .tring-hangup:hover { background: #b91c1c; }
    /* Dialpad is hidden until the call is connected. Sending DTMF
       before answer would be a no-op on the FS side anyway. */
    #${PANEL_ID} .tring-panel-dialpad {
      display: none; grid-template-columns: repeat(3, 1fr); gap: .35rem;
    }
    #${PANEL_ID}[data-state="connected"] .tring-panel-dialpad { display: grid; }
    #${PANEL_ID} .tring-dtmf {
      background: #1f2937; color: #f9fafb; border: 1px solid #374151;
      padding: .55rem 0; border-radius: 6px;
      font: 16px/1 system-ui, sans-serif; font-weight: 600;
      cursor: pointer;
    }
    #${PANEL_ID} .tring-dtmf:hover { background: #374151; }
    #${PANEL_ID} .tring-dtmf:active { background: #4b5563; }
    /* Echoes the digits the user has pressed so far — handy for
       debugging "did I actually press 1 or did the touch miss?". */
    #${PANEL_ID} .tring-panel-dialed {
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      color: #fbbf24; min-height: 1.2em; font-size: 13px;
    }
  `;
  document.head.appendChild(style);
}
