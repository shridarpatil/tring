// Auto-init entry for the drop-in <script> bundle (agent.iife.js).
//
// Reads config from its own <script> tag's data-* attributes. Recommended:
//
//   <script src=".../sdk/agent.js" data-token-url="/tring/agent-token"></script>
//
// data-token-url is a same-origin endpoint on YOUR backend that checks the
// agent's session and returns Tring's GET /v1/agents/{id}/token response
// ({ token, gateway_url, ice_servers }) unchanged. The widget fetches it at
// start-up, on every reconnect and before each transfer-list request, so the
// 5-minute token never needs refreshing by hand. The browser never sees the
// API key. For a quick test, a static token works instead (no refresh):
//
//   <script src=".../sdk/agent.js"
//           data-token="<jwt>" data-gateway-url="https://gw.example.com"></script>
//
// Call control needs no backend: Hold/Resume, Transfer, Mute, DTMF and Hang up
// go over the call socket to the gateway; the transfer list comes from
// {apiBase}/v1/sdk/transfer-targets with the agent token (apiBase is derived
// from this script's src, or data-api-base). The widget does NOT place calls
// (origination is backend-only; see widget.ts).

import { bootstrap } from "./widget";
import { Tring } from "./tring";

function findOwnScript(): HTMLScriptElement | null {
  // Prefer document.currentScript when available (runs at load time).
  const cs = document.currentScript as HTMLScriptElement | null;
  if (cs && cs.tagName === "SCRIPT") return cs;
  // Fallback: last <script> with the expected data attributes.
  const candidates = document.querySelectorAll(
    "script[data-token], script[data-token-url]",
  );
  return (candidates[candidates.length - 1] as HTMLScriptElement) ?? null;
}

function readOpts() {
  const s = findOwnScript();
  if (!s) {
    throw new Error(
      "tring widget: could not find own <script> tag — add data-token or data-token-url",
    );
  }
  // data-gateway-url is optional: when omitted, the token endpoint
  // (data-token-url) supplies the gateway URL (+ ICE) in its response.
  // apiBase for SDK call-control: data-api-base wins; else derive from this
  // script's own URL (agent.js is served from {apiBase}/sdk/agent.js).
  const apiBase =
    s.getAttribute("data-api-base") ||
    (s.src ? s.src.replace(/\/sdk\/agent\.js(\?.*)?$/, "") : undefined);
  return {
    token: s.getAttribute("data-token") || undefined,
    tokenUrl: s.getAttribute("data-token-url") || undefined,
    gatewayUrl: s.getAttribute("data-gateway-url") || undefined,
    apiBase: apiBase || undefined,
  };
}

function start() {
  let opts;
  try {
    opts = readOpts();
  } catch (e) {
    console.error("[tring]", e);
    return;
  }
  bootstrap(opts).catch((e) => console.error("[tring] bootstrap failed:", e));
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start);
} else {
  start();
}

// Also expose Tring globally so integrators can drop into programmatic use
// after the widget has initialised.
(globalThis as any).Tring = Tring;
