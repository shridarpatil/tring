// Auto-init entry for the visitor web-calling widget. Built as
// dist/voice.iife.js and served by the API at /sdk/voice.js.
//
// Drop one tag on any public page; no JS required:
//
//   <script src=".../sdk/voice.js"
//           data-api-base="https://api.tring.io"
//           data-public-key="pk_live_…"
//           data-flow-id="<uuid>"        // optional; else tenant default
//           data-button-label="Call us"  // optional
//   ></script>
//
// For a LOGGED-IN caller whose identity you want on the call log, skip the
// public key and point the widget at your own backend, which returns the
// credentials it minted via Tring's authenticated endpoint (with a trusted
// client_ref):
//
//   <script src=".../sdk/voice.js"
//           data-credentials-url="/api/tring/guest-credentials"
//   ></script>
//
// Renders a floating "Call support" launcher + full-screen call UI. To use
// your own button instead, add data-render-button="false" and put
// `<button data-tring-guest-call>` anywhere on the page.

import { bootstrapGuest } from "./guest-widget";
import { TringGuest } from "./guest";

function findOwnScript(): HTMLScriptElement | null {
  const cs = document.currentScript as HTMLScriptElement | null;
  if (cs && cs.tagName === "SCRIPT") return cs;
  const candidates = document.querySelectorAll(
    "script[data-public-key], script[data-credentials-url], script[data-gateway-url]",
  );
  return (candidates[candidates.length - 1] as HTMLScriptElement) ?? null;
}

function start() {
  const s = findOwnScript();
  if (!s) {
    console.error(
      "[tring-guest] could not find own <script> tag (needs data-gateway-url, data-public-key, or data-credentials-url)",
    );
    return;
  }
  const apiBase =
    s.getAttribute("data-api-base") || s.getAttribute("data-api") || "";
  const publicKey = s.getAttribute("data-public-key") || "";
  const credentialsUrl = s.getAttribute("data-credentials-url") || "";
  const gatewayUrl = s.getAttribute("data-gateway-url") || "";
  // Gateway path (preferred), bring-your-own-credentials (trusted), or the
  // public mint — any one is sufficient.
  if (!gatewayUrl && !credentialsUrl && (!apiBase || !publicKey)) {
    console.error(
      "[tring-guest] provide data-gateway-url, data-credentials-url, or data-api-base + data-public-key",
    );
    return;
  }
  bootstrapGuest({
    apiBase: apiBase || undefined,
    publicKey: publicKey || undefined,
    credentialsUrl: credentialsUrl || undefined,
    gatewayUrl: gatewayUrl || undefined,
    flowId: s.getAttribute("data-flow-id") || undefined,
    buttonLabel: s.getAttribute("data-button-label") || undefined,
    title: s.getAttribute("data-title") || undefined,
    renderButton: s.getAttribute("data-render-button") !== "false",
  });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start);
} else {
  start();
}

// Expose for programmatic use after auto-init.
(globalThis as any).TringGuest = TringGuest;
