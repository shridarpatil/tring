// Auto-init entry for the drop-in <script> bundle (voice.iife.js).
//
// Reads config from its own <script> tag's data-* attributes:
//
//   <script src=".../voice.js"
//           data-api-base="https://api.tring.io"
//           data-token="<jwt>" />
//
// or with a refresh endpoint:
//
//   <script src=".../voice.js"
//           data-api-base="https://api.tring.io"
//           data-token-url="/tring/token" />
//
// The token endpoint should return `{ "token": "<jwt>" }` after the
// integrating server mints a fresh JWT for the current agent (using its
// tenant API key). The browser never sees the API key.

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
  const apiBase =
    s.getAttribute("data-api-base") || s.getAttribute("data-api") || "";
  if (!apiBase) {
    throw new Error("tring widget: data-api-base is required");
  }
  return {
    apiBase,
    token: s.getAttribute("data-token") || undefined,
    tokenUrl: s.getAttribute("data-token-url") || undefined,
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
