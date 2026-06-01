// Auto-init entry for the drop-in guest <script> bundle (support.iife.js).
//
// Drop one tag on a public support page; no JS required:
//
//   <script src=".../support.js"
//           data-api-base="https://api.tring.io"
//           data-public-key="pk_live_…"
//           data-flow-id="<uuid>"        // optional; else tenant default
//           data-button-label="Call us"  // optional
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
  const candidates = document.querySelectorAll("script[data-public-key]");
  return (candidates[candidates.length - 1] as HTMLScriptElement) ?? null;
}

function start() {
  const s = findOwnScript();
  if (!s) {
    console.error(
      "[tring-guest] could not find own <script> tag (needs data-public-key)",
    );
    return;
  }
  const apiBase =
    s.getAttribute("data-api-base") || s.getAttribute("data-api") || "";
  const publicKey = s.getAttribute("data-public-key") || "";
  if (!apiBase || !publicKey) {
    console.error("[tring-guest] data-api-base and data-public-key are required");
    return;
  }
  bootstrapGuest({
    apiBase,
    publicKey,
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
