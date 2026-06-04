// Auto-init entry for the drop-in <script> bundle (agent.iife.js).
//
// Reads config from its own <script> tag's data-* attributes:
//
//   <script src=".../sdk/agent.js"
//           data-gateway-url="https://gw.tring.io"
//           data-token="<jwt>" />
//
// or with a refresh endpoint:
//
//   <script src=".../sdk/agent.js"
//           data-gateway-url="https://gw.tring.io"
//           data-token-url="/tring/token" />
//
// The token endpoint should return `{ "token": "<jwt>" }` after the
// integrating server mints a fresh JWT for the current agent (using its
// tenant API key). The browser never sees the API key. The widget only
// connects the gateway WebSocket and renders the in-call panel — it does NOT
// place calls (origination is backend-only; see widget.ts).

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
  return {
    token: s.getAttribute("data-token") || undefined,
    tokenUrl: s.getAttribute("data-token-url") || undefined,
    gatewayUrl: s.getAttribute("data-gateway-url") || undefined,
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
