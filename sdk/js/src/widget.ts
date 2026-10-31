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
const ICON_SCREEN = `<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor"><path d="M20 4H4a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h6v2H8v2h8v-2h-2v-2h6a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2zm0 12H4V6h16v10z"/></svg>`;
const ICON_MINIMIZE = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M5 12h14"/></svg>`;
const ICON_EXPAND = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 4H4v5M15 4h5v5M9 20H4v-5M15 20h5v-5"/></svg>`;

// Audio bars: the same bars and delays as the call card on the Tring website.
// The lighter "alt" bars stand for the agent's mic and go flat while muted.
const WAVE_HTML = (
  [[0, ""], [0.15, ""], [0.3, ""], [0.45, "alt"], [0.6, "alt"], [0.75, ""], [0.9, ""], [0.55, "alt"], [0.2, ""], [0.7, ""]] as const
)
  .map(([d, c]) => `<span class="${c}" style="animation-delay:${d}s"></span>`)
  .join("");

// Header text for non-timer states; anything else is shown as given.
const STATUS_TEXT: Record<string, string> = { calling: "Calling…" };

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
  // Connected + backend-marked Away shows Away, not Online.
  const connectedState = () => (tring.available ? "online" : "away");
  panel.setStatus("idle", "ready");
  badge.set(connectedState());

  tring.on("registered", () => {
    panel.setStatus("idle", "ready");
    badge.set(connectedState());
  });
  tring.on("availability", () => badge.set(connectedState()));
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
  tring.on("call:remote-video", ({ stream }) => panel.setRemoteVideo(stream));
  tring.on("call:supervisor", ({ present, mode, name }) => panel.setSupervisor(present, mode, name));
  tring.on("call:ended", () => panel.hide()); // hide() resets the supervisor badge too
  tring.on("error", ({ error }) => panel.setStatus("error", error.message));

  return tring;
}

// createStatusBadge renders an always-visible status indicator (online / away /
// reconnecting / offline). Away = connected but marked unavailable by the
// integrator's backend (POST /v1/agents/{id}/availability), pushed live.
interface StatusBadge {
  root: HTMLElement;
  set(state: "online" | "away" | "reconnecting" | "offline", attempt?: number, label?: string): void;
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
          : state === "away"
            ? "Away"
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
  setRemoteVideo(stream: MediaStream | null): void;
  setSupervisor(present: boolean, mode: string, name: string): void;
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

  // Call card: a status line (dot + state or live timer), the caller, a "stage"
  // that shows ONE of audio bars / hold notice / the caller's shared screen /
  // keypad / transfer picker (see updateStage), then the controls. Minimized
  // during a connected call it collapses to the .tring-mini pill.
  root.innerHTML = `
    <div class="tring-mini">
      <span class="tring-dot"></span>
      <span class="tring-mini-name"></span>
      <span class="tring-mini-time"></span>
      <button type="button" class="tring-mini-btn tring-mini-mute" title="Mute" aria-label="Mute">${ICON_MIC}</button>
      <button type="button" class="tring-mini-btn tring-expand" title="Expand" aria-label="Expand">${ICON_EXPAND}</button>
      <button type="button" class="tring-mini-btn tring-mini-end" title="Hang up" aria-label="Hang up">${ICON_PHONE}</button>
    </div>
    <div class="tring-card">
      <div class="tring-panel-status">
        <span class="tring-dot"></span>
        <span class="tring-panel-sub"></span>
        <button type="button" class="tring-minimize" title="Minimize" aria-label="Minimize">${ICON_MINIMIZE}</button>
      </div>
      <div class="tring-panel-header">
        <div class="tring-panel-avatar"></div>
        <div class="tring-panel-to"></div>
      </div>
      <div class="tring-panel-supervisor"></div>
      <div class="tring-panel-stage">
        <div class="tring-wave" aria-hidden="true">${WAVE_HTML}</div>
        <div class="tring-stage-msg tring-msg-ringing" aria-hidden="true"><i></i><i></i><i></i></div>
        <div class="tring-stage-msg tring-msg-hold"><strong>Caller is on hold</strong><span>They hear hold music.</span></div>
        <div class="tring-panel-video">
          <video class="tring-remote-video" autoplay playsinline></video>
          <div class="tring-video-bar"><span>Caller is sharing their screen</span><button type="button" class="tring-fullscreen">Full screen</button></div>
        </div>
        <div class="tring-panel-dialpad">
          <div class="tring-panel-dialed"></div>
          <div class="tring-dtmf-grid">${dtmfButtonsHTML}</div>
        </div>
        <div class="tring-panel-picker"></div>
      </div>
      <div class="tring-panel-incoming">
        <button type="button" class="tring-ctl tring-decline">Decline</button>
        <button type="button" class="tring-ctl tring-accept">Accept</button>
      </div>
      <div class="tring-panel-actions">
        <button type="button" class="tring-ctl tring-mute" title="Mute" aria-label="Mute">${ICON_MIC}</button>
        <button type="button" class="tring-ctl tring-hold" title="Hold" aria-label="Hold">${ICON_HOLD}</button>
        <button type="button" class="tring-ctl tring-keypad" title="Keypad" aria-label="Keypad">${ICON_KEYPAD}</button>
        <button type="button" class="tring-ctl tring-transfer" title="Transfer" aria-label="Transfer">${ICON_TRANSFER}</button>
        <button type="button" class="tring-ctl tring-share" title="Share screen" aria-label="Share screen">${ICON_SCREEN}</button>
        <button type="button" class="tring-ctl tring-hangup" title="Hang up" aria-label="Hang up">End</button>
      </div>
    </div>
  `;

  const avatarEl = root.querySelector<HTMLElement>(".tring-panel-avatar")!;
  const miniNameEl = root.querySelector<HTMLElement>(".tring-mini-name")!;
  const miniTimeEl = root.querySelector<HTMLElement>(".tring-mini-time")!;
  const miniMuteBtn = root.querySelector<HTMLButtonElement>(".tring-mini-mute")!;
  const toEl = root.querySelector<HTMLElement>(".tring-panel-to")!;
  const subEl = root.querySelector<HTMLElement>(".tring-panel-sub")!;
  const supervisorEl = root.querySelector<HTMLElement>(".tring-panel-supervisor")!;
  const dialedEl = root.querySelector<HTMLElement>(".tring-panel-dialed")!;
  const hangupBtn = root.querySelector<HTMLButtonElement>(".tring-hangup")!;
  const muteBtn = root.querySelector<HTMLButtonElement>(".tring-mute")!;
  const holdBtn = root.querySelector<HTMLButtonElement>(".tring-hold")!;
  const transferBtn = root.querySelector<HTMLButtonElement>(".tring-transfer")!;
  const keypadBtn = root.querySelector<HTMLButtonElement>(".tring-keypad")!;
  const shareBtn = root.querySelector<HTMLButtonElement>(".tring-share")!;
  const remoteVideo = root.querySelector<HTMLVideoElement>(".tring-remote-video")!;
  const pickerEl = root.querySelector<HTMLElement>(".tring-panel-picker")!;
  const dialpadEl = root.querySelector<HTMLElement>(".tring-panel-dialpad")!;
  const acceptBtn = root.querySelector<HTMLButtonElement>(".tring-accept")!;
  const declineBtn = root.querySelector<HTMLButtonElement>(".tring-decline")!;

  let held = false;

  // setState is the only writer of data-state, so the stage always follows it.
  function setState(state: string) {
    root.setAttribute("data-state", state);
    updateStage();
  }
  // The stage shows one thing at a time: an open keypad or transfer picker
  // wins, then the caller's shared screen, then ringing dots, hold notice or
  // the audio bars. "none" hides the stage.
  function updateStage() {
    const state = root.getAttribute("data-state");
    const has = (c: string) => root.classList.contains(c);
    let stage = "none";
    if (has("tring-keypad-open")) stage = "keypad";
    else if (has("tring-picker-open")) stage = "picker";
    else if (has("tring-has-video")) stage = "video";
    else if (state === "calling" || state === "connecting") stage = "ringing";
    else if (state === "connected") stage = held ? "hold" : "wave";
    root.setAttribute("data-stage", stage);
  }

  // Caller label in the card and the mini pill; the avatar shows its first
  // letter, or a person icon when the label doesn't start with one.
  function setCaller(label: string) {
    toEl.textContent = label;
    miniNameEl.textContent = label;
    const first = label.trim().charAt(0);
    if (/\p{L}/u.test(first)) avatarEl.textContent = first.toUpperCase();
    else avatarEl.innerHTML = ICON_USER;
  }

  // Call-duration timer: ticks MM:SS into the status line once connected.
  let timerId: number | undefined;
  let startMs = 0;
  function fmt(s: number) {
    const m = Math.floor(s / 60);
    return `${String(m).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
  }
  function elapsed() {
    return fmt(Math.floor((Date.now() - startMs) / 1000));
  }
  function renderTime() {
    if (!timerId) return;
    const t = elapsed();
    subEl.textContent = `${held ? "On hold" : "Live call"} · ${t}`;
    miniTimeEl.textContent = held ? "On hold" : t;
  }
  function startTimer() {
    stopTimer();
    startMs = Date.now();
    timerId = window.setInterval(renderTime, 1000);
    renderTime();
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
    syncMute(false);
    held = false;
    root.classList.remove("tring-held");
    root.classList.remove("tring-min");
    transferBtn.classList.remove("is-active");
    holdBtn.innerHTML = ICON_HOLD;
    holdBtn.title = "Hold";
    holdBtn.classList.remove("is-active");
    holdBtn.disabled = false;
    transferBtn.disabled = false;
    keypadBtn.classList.remove("is-active");
    shareBtn.innerHTML = ICON_SCREEN;
    shareBtn.title = "Share screen";
    shareBtn.classList.remove("is-active");
    shareBtn.disabled = false;
    remoteVideo.srcObject = null;
    root.classList.remove("tring-has-video");
    root.classList.remove("tring-keypad-open");
    root.classList.remove("tring-picker-open");
    dialedEl.textContent = "";
    dialed = "";
    supervisorEl.style.display = "none";
    supervisorEl.textContent = "";
    updateStage();
  }

  // syncMute mirrors the mic state onto both mute buttons (card + mini pill)
  // and flattens the agent's audio bars.
  function syncMute(muted: boolean) {
    for (const b of [muteBtn, miniMuteBtn]) {
      b.innerHTML = muted ? ICON_MIC_OFF : ICON_MIC;
      b.title = muted ? "Unmute" : "Mute";
      b.setAttribute("aria-label", b.title);
      b.classList.toggle("is-active", muted);
    }
    root.classList.toggle("tring-muted", muted);
  }

  const ringer = new Ringer();
  let active: Call | null = null;
  let dialed = "";
  // After a connected call ends the card lingers on "Call ended · MM:SS".
  let endTimer: number | undefined;
  function cancelEndLinger() {
    if (endTimer) {
      clearTimeout(endTimer);
      endTimer = undefined;
    }
  }

  hangupBtn.addEventListener("click", async () => {
    if (active) await active.hangup();
  });
  muteBtn.addEventListener("click", () => {
    if (!active) return;
    const next = !active.muted;
    active.setMuted(next);
    syncMute(next);
  });
  // The mini pill's buttons drive the same handlers as the card's.
  miniMuteBtn.addEventListener("click", () => muteBtn.click());
  root.querySelector(".tring-mini-end")!.addEventListener("click", () => hangupBtn.click());
  root.querySelector(".tring-minimize")!.addEventListener("click", () => root.classList.add("tring-min"));
  root.querySelector(".tring-expand")!.addEventListener("click", () => root.classList.remove("tring-min"));
  root.querySelector(".tring-fullscreen")!.addEventListener("click", () => {
    remoteVideo.requestFullscreen?.().catch(() => {});
  });
  keypadBtn.addEventListener("click", () => {
    const open = root.classList.toggle("tring-keypad-open");
    keypadBtn.classList.toggle("is-active", open);
    if (open) closePicker();
    updateStage();
  });
  // Screen share: toggle the agent's own screen to the caller. getDisplayMedia
  // must run in this click gesture; on cancel we just restore the button.
  shareBtn.addEventListener("click", async () => {
    if (!active) return;
    if (active.sharingScreen) {
      active.stopScreen();
      shareBtn.innerHTML = ICON_SCREEN;
      shareBtn.title = "Share screen";
      shareBtn.classList.remove("is-active");
      return;
    }
    shareBtn.disabled = true;
    try {
      await active.shareScreen();
      shareBtn.title = "Stop sharing";
      shareBtn.classList.add("is-active");
    } catch {
      /* user cancelled the screen-picker */
    } finally {
      shareBtn.disabled = false;
    }
  });
  holdBtn.addEventListener("click", () => {
    if (!active) return;
    // Hold/resume act on the live call over /rtc (the brain injects MOH); the
    // edit is optimistic — there's no round-trip ack.
    const want = !held;
    if (want) active.hold();
    else active.resume();
    held = want;
    holdBtn.innerHTML = held ? ICON_RESUME : ICON_HOLD;
    holdBtn.title = held ? "Resume" : "Hold";
    holdBtn.setAttribute("aria-label", holdBtn.title);
    holdBtn.classList.toggle("is-active", held);
    root.classList.toggle("tring-held", held);
    renderTime();
    updateStage();
  });
  // Transfer: open a picker of online agents + teams (fetched live), pick one
  // to blind-transfer the caller. On success the original agent's leg ends.
  // Clicking Transfer again closes it.
  function closePicker() {
    root.classList.remove("tring-picker-open");
    transferBtn.classList.remove("is-active");
    updateStage();
  }
  transferBtn.addEventListener("click", async () => {
    if (!active || !control) return;
    if (root.classList.contains("tring-picker-open")) {
      closePicker();
      return;
    }
    root.classList.remove("tring-keypad-open");
    keypadBtn.classList.remove("is-active");
    pickerEl.innerHTML = `<div class="tring-picker-title">Loading…</div>`;
    root.classList.add("tring-picker-open");
    transferBtn.classList.add("is-active");
    updateStage();
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
  pickerEl.addEventListener("click", (ev) => {
    const btn = (ev.target as HTMLElement).closest<HTMLButtonElement>("button");
    if (!btn) return;
    if (btn.classList.contains("tring-picker-cancel")) {
      closePicker();
      return;
    }
    if (!btn.classList.contains("tring-picker-item") || !active) return;
    closePicker();
    // Blind-transfer over /rtc; when the target answers, the brain drops this
    // leg → "call:ended". The target list still comes from the API (targets()).
    active.transfer(btn.dataset.sip ? { toSip: btn.dataset.sip } : { toTeam: btn.dataset.team });
  });
  acceptBtn.addEventListener("click", async () => {
    ringer.stop();
    if (!active) return;
    // The WebRTC handshake (mic prompt + ICE gathering + offer/answer) takes a
    // visible beat. Flip to a "connecting" state on the click itself so the
    // button never feels ignored; "connected" replaces it when media is up.
    subEl.textContent = "Connecting…";
    setState("connecting");
    try {
      await active.accept();
    } catch {
      // accept() failures surface via the error event → panel.setStatus("error").
    }
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
      cancelEndLinger();
      active = call;
      stopTimer();
      resetControls();
      setCaller(call.to);
      subEl.textContent = STATUS_TEXT[status] ?? status;
      setState(status);
      root.classList.add("tring-panel-visible");
    },
    showIncoming(call) {
      cancelEndLinger();
      active = call;
      stopTimer();
      resetControls();
      setCaller(call.displayName || call.to || "Unknown caller");
      subEl.textContent = "Incoming call";
      setState("incoming");
      root.classList.add("tring-panel-visible");
      ringer.start();
    },
    setActive(call, status) {
      active = call;
      ringer.stop();
      setState(status);
      // Connected → run the call timer; any other transition shows the text.
      if (status === "connected") startTimer();
      else subEl.textContent = STATUS_TEXT[status] ?? status;
    },
    setStatus(state, text) {
      if (timerId && state === "connected") return; // don't clobber the timer
      subEl.textContent = text;
      setState(state);
    },
    setRemoteVideo(stream) {
      remoteVideo.srcObject = stream;
      root.classList.toggle("tring-has-video", stream !== null);
      updateStage();
    },
    setSupervisor(present, mode, name) {
      if (!present) {
        supervisorEl.style.display = "none";
        supervisorEl.textContent = "";
        return;
      }
      supervisorEl.textContent = mode === "whisper" ? `Whisper: ${name}` : "🎧 Supervising";
      supervisorEl.style.display = "block";
    },
    hide() {
      ringer.stop();
      // A call that was never connected (declined, missed, picked up
      // elsewhere) closes at once, as before. A connected one shows
      // "Call ended · MM:SS" for 2s first; controls are inert (active = null).
      const spent = timerId ? elapsed() : "";
      stopTimer();
      active = null;
      resetControls();
      cancelEndLinger();
      if (!spent) {
        root.classList.remove("tring-panel-visible");
        return;
      }
      subEl.textContent = `Call ended · ${spent}`;
      setState("ended");
      endTimer = window.setTimeout(() => {
        endTimer = undefined;
        root.classList.remove("tring-panel-visible");
      }, 2000);
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
      width: 360px; max-width: calc(100vw - 2rem); box-sizing: border-box;
      color: #1f2420; font: 14px/1.4 system-ui, -apple-system, "Segoe UI", sans-serif;
      transform: translateY(180%); opacity: 0;
      transition: transform .2s ease, opacity .2s ease;
      z-index: 999999;
    }
    #${PANEL_ID} * { box-sizing: border-box; }
    #${PANEL_ID}.tring-panel-visible {
      transform: translateY(0); opacity: 1;
    }
    #${PANEL_ID} button { font: inherit; }
    #${PANEL_ID} button:focus-visible { outline: 2px solid #0f5c46; outline-offset: 2px; }
    #${PANEL_ID} .tring-card {
      background: #fbfaf7; border: 1px solid #e4e0d5; border-radius: 22px;
      box-shadow: 0 18px 40px rgba(31,36,32,.12), 0 2px 6px rgba(31,36,32,.06);
      padding: 20px; display: flex; flex-direction: column; gap: 16px;
    }
    /* Status line: dot + state text, or "Live call · MM:SS" once connected. */
    #${PANEL_ID} .tring-panel-status { display: flex; align-items: center; gap: 10px; min-height: 30px; }
    #${PANEL_ID} .tring-dot { width: 9px; height: 9px; border-radius: 50%; flex: none; background: #9a9c95; }
    #${PANEL_ID}[data-state="incoming"] .tring-dot,
    #${PANEL_ID}[data-state="connected"] .tring-dot { background: #0f5c46; }
    #${PANEL_ID}[data-state="calling"] .tring-dot,
    #${PANEL_ID}[data-state="connecting"] .tring-dot,
    #${PANEL_ID}.tring-held .tring-dot { background: #d98e04; }
    #${PANEL_ID}[data-state="error"] .tring-dot { background: #c0392b; }
    /* Connecting: pulse so the wait reads as "working", not "stuck". */
    #${PANEL_ID}[data-state="connecting"] .tring-dot { animation: tring-pulse 1s ease-in-out infinite; }
    #${PANEL_ID} .tring-panel-sub {
      flex: 1; min-width: 0; font-weight: 600; font-size: 15px;
      font-variant-numeric: tabular-nums;
      white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    }
    #${PANEL_ID}.tring-held .tring-panel-sub { color: #8a4b00; }
    #${PANEL_ID}[data-state="ended"] .tring-panel-sub { color: #5c6058; }
    #${PANEL_ID}[data-state="error"] .tring-panel-sub { color: #a3271b; white-space: normal; }
    #${PANEL_ID} .tring-minimize {
      display: none; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 8px;
      background: transparent; color: #5c6058; cursor: pointer;
      align-items: center; justify-content: center;
    }
    #${PANEL_ID} .tring-minimize:hover { background: #ece9e0; }
    #${PANEL_ID}[data-state="connected"] .tring-minimize { display: inline-flex; }
    /* Caller: square avatar with the label's first letter, then the label. */
    #${PANEL_ID} .tring-panel-header { display: flex; align-items: center; gap: 14px; }
    #${PANEL_ID} .tring-panel-avatar {
      width: 52px; height: 52px; border-radius: 14px; flex: none;
      background: #0f5c46; color: #fff;
      display: flex; align-items: center; justify-content: center;
      font: 600 24px/1 Georgia, "Times New Roman", serif;
    }
    #${PANEL_ID}[data-state="ended"] .tring-panel-avatar { background: #8e918a; }
    #${PANEL_ID} .tring-panel-to { min-width: 0; font-weight: 600; font-size: 18px; overflow-wrap: anywhere; }
    /* Supervisor indicator — a console operator is monitoring/whispering/barging.
       Hidden by default; shown/hidden via panel.setSupervisor(). */
    #${PANEL_ID} .tring-panel-supervisor {
      display: none; width: fit-content;
      background: #e3ede7; color: #0f5c46;
      font-size: 12px; font-weight: 600;
      padding: .25rem .65rem; border-radius: 999px;
    }
    /* Stage: one view at a time, picked by data-stage (see updateStage). */
    #${PANEL_ID} .tring-panel-stage { background: #f6f3ea; border-radius: 14px; }
    #${PANEL_ID}[data-stage="none"] .tring-panel-stage,
    #${PANEL_ID} .tring-panel-stage > * { display: none; }
    #${PANEL_ID}[data-stage="wave"] .tring-wave,
    #${PANEL_ID}[data-stage="ringing"] .tring-msg-ringing,
    #${PANEL_ID}[data-stage="hold"] .tring-msg-hold { display: flex; }
    #${PANEL_ID}[data-stage="video"] .tring-panel-video,
    #${PANEL_ID}[data-stage="keypad"] .tring-panel-dialpad,
    #${PANEL_ID}[data-stage="picker"] .tring-panel-picker { display: block; }
    #${PANEL_ID} .tring-wave { height: 76px; align-items: center; justify-content: center; gap: 3px; }
    #${PANEL_ID} .tring-wave span {
      width: 3px; height: 56px; border-radius: 3px; background: #0f5c46;
      animation: tring-wave 1.1s ease-in-out infinite;
    }
    #${PANEL_ID} .tring-wave span.alt { background: #3f8a6f; }
    #${PANEL_ID}.tring-muted .tring-wave span.alt { animation: none; transform: scaleY(.08); }
    #${PANEL_ID} .tring-stage-msg {
      height: 76px; padding: 0 12px; flex-direction: column; gap: 4px;
      align-items: center; justify-content: center; text-align: center;
    }
    #${PANEL_ID} .tring-msg-hold strong { font-size: 15px; color: #8a4b00; }
    #${PANEL_ID} .tring-msg-hold span { font-size: 13px; color: #5c6058; }
    #${PANEL_ID} .tring-msg-ringing { flex-direction: row; gap: 6px; }
    #${PANEL_ID} .tring-msg-ringing i {
      width: 7px; height: 7px; border-radius: 50%; background: #d98e04;
      animation: tring-pulse 1.2s ease-in-out infinite;
    }
    #${PANEL_ID} .tring-msg-ringing i:nth-child(2) { animation-delay: .2s; }
    #${PANEL_ID} .tring-msg-ringing i:nth-child(3) { animation-delay: .4s; }
    /* The caller's shared screen (a web caller's video track). */
    #${PANEL_ID} .tring-panel-video { padding: 8px; }
    #${PANEL_ID} .tring-remote-video {
      display: block; width: 100%; max-height: 200px;
      border-radius: 10px; background: #1f2420; object-fit: contain;
    }
    #${PANEL_ID} .tring-video-bar {
      display: flex; align-items: center; gap: 8px; padding: 8px 4px 0;
      font-size: 13px; font-weight: 600;
    }
    #${PANEL_ID} .tring-video-bar span { flex: 1; }
    #${PANEL_ID} .tring-fullscreen {
      height: 30px; padding: 0 10px; border: 1px solid #dcd8cc; border-radius: 8px;
      background: #fbfaf7; color: #1f2420; font-size: 13px; font-weight: 600; cursor: pointer;
    }
    /* Keypad (dialpad), opened from the keypad button. */
    #${PANEL_ID} .tring-panel-dialpad { padding: 12px; }
    /* Echoes the digits the user has pressed so far — handy for
       debugging "did I actually press 1 or did the touch miss?". */
    #${PANEL_ID} .tring-panel-dialed {
      min-height: 22px; margin-bottom: 10px; text-align: center;
      font: 17px/22px ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: .2em;
    }
    #${PANEL_ID} .tring-dtmf-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px; }
    #${PANEL_ID} .tring-dtmf {
      height: 44px; border: 0; border-radius: 10px;
      background: #fbfaf7; color: #1f2420; cursor: pointer;
      font: 18px/1 ui-monospace, SFMono-Regular, Menlo, monospace;
    }
    #${PANEL_ID} .tring-dtmf:hover { background: #fff; }
    #${PANEL_ID} .tring-dtmf:active { background: #ece9e0; }
    /* Transfer picker: a live list of agents + teams. */
    #${PANEL_ID} .tring-panel-picker { padding: 12px; max-height: 240px; overflow-y: auto; }
    #${PANEL_ID} .tring-picker-title,
    #${PANEL_ID} .tring-picker-sub {
      font-size: 12px; font-weight: 700; color: #5c6058;
      letter-spacing: .05em; text-transform: uppercase; padding: 2px 4px 6px;
    }
    #${PANEL_ID} .tring-picker-sub { padding-top: 8px; }
    #${PANEL_ID} .tring-picker-empty { font-size: 13px; color: #5c6058; padding: 4px 4px 8px; }
    #${PANEL_ID} .tring-picker-item {
      display: block; width: 100%; min-height: 42px; margin-bottom: 6px; padding: 0 12px;
      border: 0; border-radius: 10px; background: #fbfaf7; color: #1f2420;
      text-align: left; font-weight: 600; cursor: pointer;
    }
    #${PANEL_ID} .tring-picker-item:hover { background: #fff; }
    #${PANEL_ID} .tring-picker-cancel {
      width: 100%; padding: 6px; border: 0; background: none;
      color: #5c6058; cursor: pointer; text-decoration: underline;
    }
    /* Controls. Accept/Decline only while an inbound call rings. */
    #${PANEL_ID} .tring-panel-incoming { display: none; grid-template-columns: 1fr 1fr; gap: 8px; }
    #${PANEL_ID}[data-state="incoming"] .tring-panel-incoming { display: grid; }
    #${PANEL_ID} .tring-panel-actions { display: flex; gap: 6px; }
    #${PANEL_ID}[data-state="incoming"] .tring-panel-actions,
    #${PANEL_ID}[data-state="ended"] .tring-panel-actions { display: none; }
    #${PANEL_ID} .tring-ctl {
      flex: 1 1 0; min-width: 0; height: 52px; padding: 0;
      border: 0; border-radius: 14px; cursor: pointer;
      display: inline-flex; align-items: center; justify-content: center;
      background: #ece9e0; color: #2b302b;
    }
    #${PANEL_ID} .tring-ctl svg { width: 20px; height: 20px; }
    #${PANEL_ID} .tring-ctl:hover { background: #e3dfd3; }
    #${PANEL_ID} .tring-ctl:disabled { opacity: .5; cursor: default; }
    #${PANEL_ID} .tring-ctl.is-active { background: #0f5c46; color: #fff; }
    #${PANEL_ID} .tring-hangup, #${PANEL_ID} .tring-accept, #${PANEL_ID} .tring-decline {
      font-size: 16px; font-weight: 700;
    }
    #${PANEL_ID} .tring-hangup { flex: 1.8 1 0; background: #c0392b; color: #fff; }
    #${PANEL_ID} .tring-hangup:hover { background: #a93226; }
    #${PANEL_ID} .tring-accept { background: #0f5c46; color: #fff; }
    #${PANEL_ID} .tring-accept:hover { background: #0b4736; }
    #${PANEL_ID} .tring-decline { background: #f6e3e0; color: #a3271b; }
    #${PANEL_ID} .tring-decline:hover { background: #f0d3ce; }
    /* Minimized (connected calls only): the card collapses to a pill. */
    #${PANEL_ID} .tring-mini {
      display: none; align-items: center; gap: 10px; height: 56px; padding: 0 8px 0 18px;
      background: #fbfaf7; border: 1px solid #e4e0d5; border-radius: 999px;
      box-shadow: 0 12px 30px rgba(31,36,32,.12), 0 2px 6px rgba(31,36,32,.06);
    }
    #${PANEL_ID}.tring-min[data-state="connected"] { width: auto; }
    #${PANEL_ID}.tring-min[data-state="connected"] .tring-mini { display: flex; }
    #${PANEL_ID}.tring-min[data-state="connected"] .tring-card { display: none; }
    #${PANEL_ID} .tring-mini-name {
      max-width: 140px; font-weight: 600; font-size: 15px;
      white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    }
    #${PANEL_ID} .tring-mini-time {
      flex: 1; white-space: nowrap; color: #5c6058;
      font: 13px ui-monospace, SFMono-Regular, Menlo, monospace;
    }
    #${PANEL_ID} .tring-mini-btn {
      width: 40px; height: 40px; flex: none; padding: 0; border: 0; border-radius: 50%;
      display: inline-flex; align-items: center; justify-content: center;
      background: #ece9e0; color: #2b302b; cursor: pointer;
    }
    #${PANEL_ID} .tring-mini-btn svg { width: 18px; height: 18px; }
    #${PANEL_ID} .tring-mini-btn.is-active { background: #0f5c46; color: #fff; }
    #${PANEL_ID} .tring-mini-end { background: #c0392b; color: #fff; }
    /* The phone icon rotated to the "end call" angle. */
    #${PANEL_ID} .tring-mini-end svg { transform: rotate(135deg); }
    @keyframes tring-wave { 0%,100% { transform: scaleY(.28); } 50% { transform: scaleY(1); } }
    @media (prefers-reduced-motion: reduce) {
      #${PANEL_ID} .tring-wave span, #${PANEL_ID} .tring-msg-ringing i, #${PANEL_ID} .tring-dot { animation: none; }
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
    .tring-status-badge[data-state="away"] .tring-status-dot { background: #fbbf24; }
    .tring-status-badge[data-state="reconnecting"] .tring-status-dot {
      background: #fbbf24; animation: tring-pulse 1s ease-in-out infinite;
    }
    .tring-status-badge[data-state="offline"] .tring-status-dot { background: #f87171; }
    @keyframes tring-pulse { 0%,100% { opacity: 1; } 50% { opacity: .3; } }
  `;
  document.head.appendChild(style);
}
