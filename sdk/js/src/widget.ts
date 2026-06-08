// Agent call panel. Imports the Tring SDK, connects the gateway WebSocket, and
// renders a small floating call panel (ringing / Accept-Decline / connected +
// hangup + DTMF) whenever a leg rings this agent.
//
// The SDK has NO Tring API access, so this widget does NOT place calls. Your
// CRM's own "Call" button tells YOUR backend, which originates via the Tring
// API (POST /v1/calls); FreeSWITCH then rings this agent's leg and the panel
// appears. The agent never sees the dialed number — only a backend-supplied
// label. See widget-entry.ts for the data-attribute reader.

import { Tring } from "./tring";
import type { Call } from "./types";

export interface WidgetOpts {
  /** JWT — preferred for short-lived static tokens. */
  token?: string;
  /** URL to GET a fresh JWT from. Server replies with `{token: "..."}`. */
  tokenUrl?: string;
  /**
   * Media gateway base URL (ADR-001). The agent connects over raw WebRTC via
   * the gateway WebSocket. Required.
   */
  gatewayUrl?: string;
  /**
   * Tring API base URL (e.g. https://api.tring.io). The Hold button calls
   * POST {apiBase}/v1/sdk/calls/hold directly with the agent JWT the widget
   * already holds — no CRM proxy. Auto-derived from this script's own src
   * (agent.js is served from {apiBase}/sdk/agent.js); set data-api-base to
   * override (e.g. when loading the SDK from a CDN).
   */
  apiBase?: string;
}

const PANEL_ID = "tring-call-panel";

// Inline SVG icons for the call controls (no asset to ship).
const ICON_MIC = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M12 14a3 3 0 0 0 3-3V5a3 3 0 0 0-6 0v6a3 3 0 0 0 3 3zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.92V21h2v-3.08A7 7 0 0 0 19 11h-2z"/></svg>`;
const ICON_MIC_OFF = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M19 11h-1.7c0 .74-.16 1.43-.43 2.05l1.23 1.23c.56-.98.9-2.09.9-3.28zM15 11.16V5a3 3 0 0 0-6 0v.18l6 5.98zM4.27 3 3 4.27l6 6V11a3 3 0 0 0 3 3c.22 0 .44-.03.65-.08l1.66 1.66c-.71.33-1.5.52-2.31.52a5 5 0 0 1-5-5H5a7 7 0 0 0 6 6.92V21h2v-3.08c.91-.13 1.77-.45 2.54-.9L19.73 21 21 19.73 4.27 3z"/></svg>`;
const ICON_HOLD = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M6 5h4v14H6zM14 5h4v14h-4z"/></svg>`;
const ICON_RESUME = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>`;
const ICON_PHONE = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M6.6 10.8c1.4 2.8 3.8 5.1 6.6 6.6l2.2-2.2c.3-.3.7-.4 1-.2 1.1.4 2.3.6 3.6.6.6 0 1 .4 1 1V20c0 .6-.4 1-1 1C10.6 21 3 13.4 3 4c0-.6.4-1 1-1h3.5c.6 0 1 .4 1 1 0 1.2.2 2.4.6 3.6.1.4 0 .8-.3 1l-2.2 2.2z"/></svg>`;
const ICON_USER = `<svg viewBox="0 0 24 24" width="34" height="34" fill="currentColor"><path d="M12 12a5 5 0 1 0 0-10 5 5 0 0 0 0 10zm0 2c-4 0-9 2-9 6v1h18v-1c0-4-5-6-9-6z"/></svg>`;
const ICON_KEYPAD = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><circle cx="5" cy="5" r="1.7"/><circle cx="12" cy="5" r="1.7"/><circle cx="19" cy="5" r="1.7"/><circle cx="5" cy="12" r="1.7"/><circle cx="12" cy="12" r="1.7"/><circle cx="19" cy="12" r="1.7"/><circle cx="5" cy="19" r="1.7"/><circle cx="12" cy="19" r="1.7"/><circle cx="19" cy="19" r="1.7"/></svg>`;
const ICON_TRANSFER = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M16 7l-1.4 1.4L17.2 11H8v2h9.2l-2.6 2.6L16 17l5-5-5-5zM4 5h7V3H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h7v-2H4V5z"/></svg>`;

// Transfer targets the agent can pick from (online agents by name + teams).
interface TransferTargets {
  agents: { sip: string; name: string }[];
  teams: string[];
}

// CallControl drives the agent's OWN active call (hold/resume, blind transfer)
// against Tring's agent-JWT endpoints. Undefined when no apiBase is known.
interface CallControl {
  hold(action: "hold" | "resume"): Promise<boolean>;
  transfer(body: { to_sip?: string; to_team?: string }): Promise<boolean>;
  targets(): Promise<TransferTargets | null>;
}

// makeCallControl posts to POST {apiBase}/v1/sdk/calls/* with a FRESH agent
// token (the 5-min JWT may have rolled over mid-call), so the widget drives call
// control directly — no CRM proxy.
function makeCallControl(
  apiBase: string | undefined,
  tokenUrl: string | undefined,
  staticToken: string,
): CallControl | undefined {
  if (!apiBase) return undefined;
  const freshToken = tokenUrl
    ? async () => (await fetchCredentials(tokenUrl)).token || staticToken
    : async () => staticToken;
  const post = async (path: string, body?: unknown): Promise<boolean> => {
    try {
      const headers: Record<string, string> = { Authorization: `Bearer ${await freshToken()}` };
      if (body) headers["Content-Type"] = "application/json";
      const res = await fetch(`${apiBase}${path}`, {
        method: "POST",
        headers,
        body: body ? JSON.stringify(body) : undefined,
      });
      return res.ok;
    } catch {
      return false;
    }
  };
  const getJSON = async (path: string): Promise<unknown> => {
    try {
      const res = await fetch(`${apiBase}${path}`, {
        headers: { Authorization: `Bearer ${await freshToken()}` },
      });
      return res.ok ? await res.json() : null;
    } catch {
      return null;
    }
  };
  return {
    hold: (action) => post(`/v1/sdk/calls/${action}`),
    transfer: (body) => post(`/v1/sdk/calls/transfer`, body),
    targets: () => getJSON(`/v1/sdk/transfer-targets`) as Promise<TransferTargets | null>,
  };
}

// escapeHTML keeps names/teams (from the backend) safe to inject into innerHTML.
function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!,
  );
}

export async function bootstrap(opts: WidgetOpts): Promise<Tring> {
  // The token endpoint can also carry the gateway URL + ICE servers (the API's
  // /v1/agents/{id}/token returns them), so the <script> tag needs only
  // data-token-url. Explicit data-token/data-gateway-url still win. Fetch when
  // either is missing.
  let token = opts.token;
  let gatewayUrl = opts.gatewayUrl;
  let iceServers: RTCIceServer[] | undefined;
  if (!token || !gatewayUrl) {
    const creds = await fetchCredentials(opts.tokenUrl);
    token = token ?? creds.token;
    gatewayUrl = gatewayUrl ?? creds.gateway_url;
    iceServers = creds.ice_servers;
  }
  if (!token) {
    throw new Error(
      "tring widget: need data-token or a data-token-url that returns {token}",
    );
  }
  if (!gatewayUrl) {
    throw new Error(
      "tring widget: gateway URL missing — set data-gateway-url, or have the token endpoint return gateway_url",
    );
  }

  injectStyles();
  const control = makeCallControl(opts.apiBase, opts.tokenUrl, token);
  const panel = createPanel(control);
  document.body.appendChild(panel.root);
  const badge = createStatusBadge();
  document.body.appendChild(badge.root);

  // getToken re-mints on every (re)connect so the agent's socket survives drops
  // past the ~5-min JWT TTL. The FIRST connect reuses the token we already
  // fetched above (no double mint); reconnects re-fetch from data-token-url.
  // A static data-token can't refresh (it'll work until it expires).
  const tokenUrl = opts.tokenUrl;
  const staticToken = token;
  let prefetched: string | undefined = token;
  const getToken = tokenUrl
    ? async () => {
        if (prefetched) {
          const t = prefetched;
          prefetched = undefined; // only the first connect reuses it
          return t;
        }
        const c = await fetchCredentials(tokenUrl);
        if (!c.token) throw new Error("tring widget: token endpoint returned no token");
        return c.token;
      }
    : () => Promise.resolve(staticToken);

  // autoAnswer:false → a TRUE inbound (IVR transfer / inbound DID) rings with
  // Accept/Decline. A backend click-to-call leg auto-answers regardless (the
  // gateway flags it per-call), so the agent who clicked isn't asked twice.
  const tring = await Tring.init({
    getToken,
    gatewayUrl,
    iceServers,
    autoAnswer: false,
  });

  // Tring.init resolves once already registered — that first `registered` fired
  // during init, before these listeners attached, so set the initial UI here.
  panel.setStatus("idle", "ready");
  badge.set("online");

  tring.on("registered", () => {
    panel.setStatus("idle", "ready");
    badge.set("online");
  });
  tring.on("reconnecting", ({ attempt }) => badge.set("reconnecting", attempt));
  tring.on("unregistered", () => {
    panel.setStatus("idle", "disconnected");
    badge.set("offline");
  });
  tring.on("call:ringing", ({ call }) => {
    if (call.direction === "inbound") panel.showIncoming(call);
    else panel.show(call, "calling");
  });
  tring.on("call:answered", ({ call }) => panel.setActive(call, "connected"));
  tring.on("call:ended", () => panel.hide());
  tring.on("error", ({ error }) => panel.setStatus("error", error.message));

  return tring;
}

// createStatusBadge renders an always-visible connection indicator (online /
// reconnecting / offline). It reflects the gateway SOCKET only — agent
// availability (Away/Break) is the integrator's own UI, driven from their
// backend via POST /v1/agents/{id}/availability.
interface StatusBadge {
  root: HTMLElement;
  set(state: "online" | "reconnecting" | "offline", attempt?: number, label?: string): void;
}

function createStatusBadge(): StatusBadge {
  const root = document.createElement("div");
  root.className = "tring-status-badge";
  root.setAttribute("data-state", "offline");
  root.innerHTML = `<span class="tring-status-dot"></span><span class="tring-status-text">Offline</span>`;
  const textEl = root.querySelector<HTMLElement>(".tring-status-text")!;
  return {
    root,
    set(state, attempt, label) {
      root.setAttribute("data-state", state);
      textEl.textContent =
        label ??
        (state === "online"
          ? "Online"
          : state === "reconnecting"
            ? `Reconnecting${attempt ? ` (${attempt})` : ""}…`
            : "Offline");
    },
  };
}

interface AgentCredentials {
  token?: string;
  gateway_url?: string;
  ice_servers?: RTCIceServer[];
}

// fetchCredentials GETs the integrator's token endpoint, which returns at least
// {token} and (passing through the Tring API) optionally {gateway_url, ice_servers}.
async function fetchCredentials(url?: string): Promise<AgentCredentials> {
  if (!url) return {};
  const r = await fetch(url, { credentials: "same-origin" });
  if (!r.ok) {
    throw new Error(`tring widget: token endpoint ${url} returned ${r.status}`);
  }
  return r.json();
}

// ---- Panel ----------------------------------------------------------

interface Panel {
  root: HTMLElement;
  show(call: Call, status: string): void;
  showIncoming(call: Call): void;
  setActive(call: Call, status: string): void;
  setStatus(state: string, text: string): void;
  hide(): void;
}

function createPanel(control?: CallControl): Panel {
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

  // Mobile-phone-style call card: avatar + caller + a sub-line that shows the
  // status while ringing and a live MM:SS timer once connected.
  root.innerHTML = `
    <div class="tring-panel-row tring-panel-header">
      <div class="tring-panel-avatar">${ICON_USER}</div>
      <div class="tring-panel-to"></div>
      <div class="tring-panel-sub"></div>
    </div>
    <div class="tring-panel-row tring-panel-dialed"></div>
    <div class="tring-panel-row tring-panel-picker"></div>
    <div class="tring-panel-row tring-panel-dialpad">${dtmfButtonsHTML}</div>
    <div class="tring-panel-row tring-panel-incoming">
      <button type="button" class="tring-ctl tring-decline" title="Decline" aria-label="Decline">${ICON_PHONE}</button>
      <button type="button" class="tring-ctl tring-accept" title="Accept" aria-label="Accept">${ICON_PHONE}</button>
    </div>
    <div class="tring-panel-row tring-panel-actions">
      <button type="button" class="tring-ctl tring-mute" title="Mute" aria-label="Mute">${ICON_MIC}</button>
      <button type="button" class="tring-ctl tring-keypad" title="Keypad" aria-label="Keypad">${ICON_KEYPAD}</button>
      <button type="button" class="tring-ctl tring-hold" title="Hold" aria-label="Hold">${ICON_HOLD}</button>
      <button type="button" class="tring-ctl tring-transfer" title="Transfer" aria-label="Transfer">${ICON_TRANSFER}</button>
      <button type="button" class="tring-ctl tring-hangup" title="Hang up" aria-label="Hang up">${ICON_PHONE}</button>
    </div>
  `;

  const toEl = root.querySelector<HTMLElement>(".tring-panel-to")!;
  const subEl = root.querySelector<HTMLElement>(".tring-panel-sub")!;
  const dialedEl = root.querySelector<HTMLElement>(".tring-panel-dialed")!;
  const hangupBtn = root.querySelector<HTMLButtonElement>(".tring-hangup")!;
  const muteBtn = root.querySelector<HTMLButtonElement>(".tring-mute")!;
  const holdBtn = root.querySelector<HTMLButtonElement>(".tring-hold")!;
  const transferBtn = root.querySelector<HTMLButtonElement>(".tring-transfer")!;
  const keypadBtn = root.querySelector<HTMLButtonElement>(".tring-keypad")!;
  const pickerEl = root.querySelector<HTMLElement>(".tring-panel-picker")!;
  const dialpadEl = root.querySelector<HTMLElement>(".tring-panel-dialpad")!;
  const acceptBtn = root.querySelector<HTMLButtonElement>(".tring-accept")!;
  const declineBtn = root.querySelector<HTMLButtonElement>(".tring-decline")!;

  let held = false;

  // Call-duration timer: ticks MM:SS into the sub-line once connected.
  let timerId: number | undefined;
  let startMs = 0;
  function fmt(s: number) {
    const m = Math.floor(s / 60);
    return `${String(m).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
  }
  function startTimer() {
    stopTimer();
    startMs = Date.now();
    const tick = () => (subEl.textContent = fmt(Math.floor((Date.now() - startMs) / 1000)));
    tick();
    timerId = window.setInterval(tick, 1000);
  }
  function stopTimer() {
    if (timerId) {
      clearInterval(timerId);
      timerId = undefined;
    }
  }

  // Reset all controls to default — called whenever a fresh call appears, since
  // each is a new object starting un-muted, un-held, keypad closed.
  function resetControls() {
    muteBtn.innerHTML = ICON_MIC;
    muteBtn.title = "Mute";
    muteBtn.classList.remove("is-active");
    held = false;
    holdBtn.innerHTML = ICON_HOLD;
    holdBtn.title = "Hold";
    holdBtn.classList.remove("is-active");
    holdBtn.disabled = false;
    transferBtn.disabled = false;
    keypadBtn.classList.remove("is-active");
    root.classList.remove("tring-keypad-open");
    root.classList.remove("tring-picker-open");
    dialedEl.textContent = "";
    dialed = "";
  }

  const ringer = new Ringer();
  let active: Call | null = null;
  let dialed = "";

  hangupBtn.addEventListener("click", async () => {
    if (active) await active.hangup();
  });
  muteBtn.addEventListener("click", () => {
    if (!active) return;
    const next = !active.muted;
    active.setMuted(next);
    muteBtn.innerHTML = next ? ICON_MIC_OFF : ICON_MIC;
    muteBtn.title = next ? "Unmute" : "Mute";
    muteBtn.classList.toggle("is-active", next);
  });
  keypadBtn.addEventListener("click", () => {
    const open = root.classList.toggle("tring-keypad-open");
    keypadBtn.classList.toggle("is-active", open);
  });
  holdBtn.addEventListener("click", async () => {
    if (!active) return;
    if (!control) {
      console.warn("[tring] Call control needs the Tring API base (auto-derived from agent.js src, or set data-api-base)");
      return;
    }
    const want = !held;
    holdBtn.disabled = true;
    const ok = await control.hold(want ? "hold" : "resume");
    holdBtn.disabled = false;
    if (ok) {
      held = want;
      holdBtn.innerHTML = held ? ICON_RESUME : ICON_HOLD;
      holdBtn.title = held ? "Resume" : "Hold";
      holdBtn.classList.toggle("is-active", held);
    }
  });
  // Transfer: open a picker of online agents + teams (fetched live), pick one
  // to blind-transfer the caller. On success the original agent's leg ends.
  function closePicker() {
    root.classList.remove("tring-picker-open");
  }
  transferBtn.addEventListener("click", async () => {
    if (!active || !control) return;
    pickerEl.innerHTML = `<div class="tring-picker-title">Loading…</div>`;
    root.classList.add("tring-picker-open");
    const t = await control.targets();
    if (!t) {
      pickerEl.innerHTML = `<div class="tring-picker-title">Couldn't load targets</div><button class="tring-picker-cancel">Back</button>`;
      return;
    }
    let html = `<div class="tring-picker-title">Transfer to</div>`;
    for (const a of t.agents) {
      html += `<button class="tring-picker-item" data-sip="${escapeHTML(a.sip)}">${escapeHTML(a.name)}</button>`;
    }
    if (t.teams.length) {
      html += `<div class="tring-picker-sub">Teams</div>`;
      for (const tm of t.teams) {
        html += `<button class="tring-picker-item" data-team="${escapeHTML(tm)}">${escapeHTML(tm)}</button>`;
      }
    }
    if (!t.agents.length && !t.teams.length) {
      html += `<div class="tring-picker-empty">No one available right now</div>`;
    }
    html += `<button class="tring-picker-cancel">Cancel</button>`;
    pickerEl.innerHTML = html;
  });
  pickerEl.addEventListener("click", async (ev) => {
    const btn = (ev.target as HTMLElement).closest<HTMLButtonElement>("button");
    if (!btn || !control) return;
    if (btn.classList.contains("tring-picker-cancel")) {
      closePicker();
      return;
    }
    if (!btn.classList.contains("tring-picker-item")) return;
    closePicker();
    const body = btn.dataset.sip ? { to_sip: btn.dataset.sip } : { to_team: btn.dataset.team };
    const ok = await control.transfer(body);
    if (!ok) {
      // eslint-disable-next-line no-alert
      alert("Transfer failed — the target may be offline.");
    }
  });
  acceptBtn.addEventListener("click", async () => {
    ringer.stop();
    if (active) await active.accept();
  });
  declineBtn.addEventListener("click", async () => {
    ringer.stop();
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
      stopTimer();
      resetControls();
      toEl.textContent = call.to;
      subEl.textContent = status;
      root.setAttribute("data-state", status);
      root.classList.add("tring-panel-visible");
    },
    showIncoming(call) {
      active = call;
      stopTimer();
      resetControls();
      toEl.textContent = call.displayName || call.to || "Unknown caller";
      subEl.textContent = "Incoming call";
      root.setAttribute("data-state", "incoming");
      root.classList.add("tring-panel-visible");
      ringer.start();
    },
    setActive(call, status) {
      active = call;
      ringer.stop();
      root.setAttribute("data-state", status);
      // Connected → run the call timer; any other transition shows the text.
      if (status === "connected") startTimer();
      else subEl.textContent = status;
    },
    setStatus(state, text) {
      if (timerId && state === "connected") return; // don't clobber the timer
      subEl.textContent = text;
      root.setAttribute("data-state", state);
    },
    hide() {
      ringer.stop();
      stopTimer();
      active = null;
      resetControls();
      root.classList.remove("tring-panel-visible");
    },
  };
}

// Ringer — a short WebAudio ring loop for incoming calls. No asset to ship.
// Browsers may block audio until a user gesture; the agent has the page
// focused, and worst case the ring is silent while the visual still rings.
class Ringer {
  private ctx: AudioContext | null = null;
  private timer: number | undefined;

  start(): void {
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
      o.frequency.value = 480;
      g.gain.value = 0.1;
      o.connect(g);
      g.connect(this.ctx.destination);
      o.start();
      o.stop(this.ctx.currentTime + 0.4);
    };
    beep();
    this.timer = window.setInterval(beep, 1200);
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

function injectStyles(): void {
  if (document.getElementById("tring-styles")) return;
  const style = document.createElement("style");
  style.id = "tring-styles";
  style.textContent = `
    #${PANEL_ID} {
      position: fixed; right: 1.5rem; bottom: 1.5rem;
      width: 320px; padding: 1rem 1.25rem;
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
    /* Mobile-phone-style header: avatar, big caller, status/timer sub-line. */
    #${PANEL_ID} .tring-panel-header {
      display: flex; flex-direction: column; align-items: center;
      gap: .15rem; text-align: center; padding: .25rem 0;
    }
    #${PANEL_ID} .tring-panel-avatar {
      width: 56px; height: 56px; border-radius: 50%; margin-bottom: .35rem;
      background: #374151; color: #9ca3af;
      display: inline-flex; align-items: center; justify-content: center;
    }
    #${PANEL_ID} .tring-panel-to { font-weight: 600; font-size: 16px; }
    #${PANEL_ID} .tring-panel-sub {
      color: #9ca3af; font-size: 13px; font-variant-numeric: tabular-nums;
    }
    #${PANEL_ID}[data-state="ringing"] .tring-panel-sub { color: #fbbf24; }
    #${PANEL_ID}[data-state="incoming"] .tring-panel-sub,
    #${PANEL_ID}[data-state="connected"] .tring-panel-sub { color: #34d399; }
    #${PANEL_ID}[data-state="error"] .tring-panel-sub { color: #f87171; }
    /* Accept/Decline shown only while an inbound call is ringing. */
    #${PANEL_ID} .tring-panel-incoming { display: none; gap: 1.75rem; justify-content: center; }
    #${PANEL_ID}[data-state="incoming"] .tring-panel-incoming { display: flex; }
    #${PANEL_ID}[data-state="incoming"] .tring-panel-actions { display: none; }
    #${PANEL_ID} .tring-panel-actions { display: flex; gap: .5rem; justify-content: center; flex-wrap: wrap; }
    /* Round icon buttons for all call controls. */
    #${PANEL_ID} .tring-ctl {
      width: 44px; height: 44px; border-radius: 50%;
      border: none; cursor: pointer; padding: 0;
      display: inline-flex; align-items: center; justify-content: center;
      background: #374151; color: #fff;
    }
    #${PANEL_ID} .tring-ctl:hover { background: #4b5563; }
    #${PANEL_ID} .tring-ctl:disabled { opacity: .5; cursor: default; }
    #${PANEL_ID} .tring-ctl.is-active { background: #f59e0b; color: #111827; }
    #${PANEL_ID} .tring-accept { background: #16a34a; }
    #${PANEL_ID} .tring-accept:hover { background: #15803d; }
    /* Decline + hang-up use the phone icon rotated to the "end call" angle. */
    #${PANEL_ID} .tring-decline, #${PANEL_ID} .tring-hangup { background: #dc2626; }
    #${PANEL_ID} .tring-decline:hover, #${PANEL_ID} .tring-hangup:hover { background: #b91c1c; }
    #${PANEL_ID} .tring-decline svg, #${PANEL_ID} .tring-hangup svg { transform: rotate(135deg); }
    /* Keypad (dialpad) hidden until the agent taps the keypad button. */
    #${PANEL_ID} .tring-panel-dialpad {
      display: none; grid-template-columns: repeat(3, 1fr); gap: .35rem;
    }
    #${PANEL_ID}.tring-keypad-open .tring-panel-dialpad { display: grid; }
    /* Transfer picker: a live list of agents + teams; replaces the controls. */
    #${PANEL_ID} .tring-panel-picker { display: none; max-height: 220px; overflow-y: auto; }
    #${PANEL_ID}.tring-picker-open .tring-panel-picker { display: block; }
    #${PANEL_ID}.tring-picker-open .tring-panel-actions,
    #${PANEL_ID}.tring-picker-open .tring-panel-dialpad { display: none; }
    #${PANEL_ID} .tring-picker-title { font-size: 13px; color: #9ca3af; margin-bottom: .35rem; }
    #${PANEL_ID} .tring-picker-sub { font-size: 11px; color: #6b7280; text-transform: uppercase; margin: .5rem 0 .25rem; }
    #${PANEL_ID} .tring-picker-empty { font-size: 13px; color: #9ca3af; padding: .5rem 0; }
    #${PANEL_ID} .tring-picker-item {
      display: block; width: 100%; text-align: left;
      background: #1f2937; color: #f9fafb; border: 1px solid #374151;
      padding: .55rem .65rem; border-radius: 6px; margin-bottom: .35rem;
      font: inherit; cursor: pointer;
    }
    #${PANEL_ID} .tring-picker-item:hover { background: #374151; }
    #${PANEL_ID} .tring-picker-cancel {
      width: 100%; background: none; color: #9ca3af; border: 0;
      padding: .4rem; font: inherit; cursor: pointer; text-decoration: underline;
    }
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
    /* Always-visible connection badge (socket health only). */
    .tring-status-badge {
      position: fixed; left: 1rem; bottom: 1rem;
      display: inline-flex; align-items: center; gap: .45rem;
      padding: .35rem .7rem; border-radius: 999px;
      background: #111827; color: #e5e7eb;
      font: 12px/1 system-ui, sans-serif; box-shadow: 0 4px 16px rgba(0,0,0,.25);
      z-index: 999998; user-select: none;
    }
    .tring-status-badge .tring-status-dot {
      width: 8px; height: 8px; border-radius: 50%; background: #6b7280;
    }
    .tring-status-badge[data-state="online"] .tring-status-dot { background: #34d399; }
    .tring-status-badge[data-state="reconnecting"] .tring-status-dot {
      background: #fbbf24; animation: tring-pulse 1s ease-in-out infinite;
    }
    .tring-status-badge[data-state="offline"] .tring-status-dot { background: #f87171; }
    @keyframes tring-pulse { 0%,100% { opacity: 1; } 50% { opacity: .3; } }
  `;
  document.head.appendChild(style);
}
