// Auto-init entry for the visitor web-calling widget. Built as
// dist/voice.iife.js and served by the API at /sdk/voice.js.
//
// Drop one tag on any public page; no JS required:
//
//   <script src=".../sdk/voice.js"
//           data-gateway-url="https://gw.tring.io"
//           data-flow-id="<uuid>"         // the IVR flow to land in
//           data-button-label="Call us"   // optional
//   ></script>
//
// For a LOGGED-IN caller whose identity you want on the call log, point the
// widget at your own backend, which returns the gateway connection details
// (gateway_url + flow_id, optional token/ICE) for the verified caller:
//
//   <script src=".../sdk/voice.js"
//           data-credentials-url="/api/tring/guest-credentials"
//   ></script>
//
// Renders a floating "Call support" launcher + full-screen call UI. To use
// your own button instead, add data-render-button="false" and put
// `<button data-tring-call>` anywhere on the page.

import { bootstrapGuest } from "./guest-widget";
import { TringGuest } from "./guest";

function findOwnScript(): HTMLScriptElement | null {
  const cs = document.currentScript as HTMLScriptElement | null;
  if (cs && cs.tagName === "SCRIPT") return cs;
  const candidates = document.querySelectorAll(
    "script[data-gateway-url], script[data-credentials-url]",
  );
  return (candidates[candidates.length - 1] as HTMLScriptElement) ?? null;
}

function start() {
  const s = findOwnScript();
  if (!s) {
    console.error(
      "[tring-guest] could not find own <script> tag (needs data-gateway-url or data-credentials-url)",
    );
    return;
  }
  const credentialsUrl = s.getAttribute("data-credentials-url") || "";
  const gatewayUrl = s.getAttribute("data-gateway-url") || "";
  // Gateway URL (anonymous path) or a backend credentials URL (trusted path).
  if (!gatewayUrl && !credentialsUrl) {
    console.error(
      "[tring-guest] provide data-gateway-url (+ data-flow-id) or data-credentials-url",
    );
    return;
  }
  bootstrapGuest({
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
