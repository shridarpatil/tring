import * as esbuild from "esbuild";

const watch = process.argv.includes("--watch");

const shared = {
  entryPoints: ["src/index.ts"],
  bundle: true,
  sourcemap: true,
  target: ["chrome100", "firefox100", "safari15", "edge100"],
  loader: { ".ts": "ts" },
  define: { "process.env.NODE_ENV": '"production"' },
};

// The drop-in <script> bundles land at the package root under their served
// names, so the npm CDN URLs are short (unpkg.com/tring-voice/voice.js) and
// match the API's /sdk/voice.js etc.
const builds = [
  // IIFE — bare SDK. Exposes a global `Tring`; integrator wires it manually.
  {
    ...shared,
    outfile: "tring.js",
    format: "iife",
    globalName: "Tring",
    footer: { js: "window.Tring = Tring.default || Tring;" },
  },
  // ESM — `import Tring from '@tring/voice'`.
  {
    ...shared,
    outfile: "dist/tring.esm.js",
    format: "esm",
  },
  // agent.js — the drop-in CRM-agent widget. Auto-binds
  // <button data-tring-call> and renders the floating call panel on DOM
  // ready, reading config from its own <script> tag's data-* attributes.
  // (Served by the API at /sdk/agent.js.)
  {
    ...shared,
    entryPoints: ["src/widget-entry.ts"],
    outfile: "agent.js",
    format: "iife",
  },
  // voice.js — the drop-in widget for putting voice calling on any website.
  // A visitor clicks "Call", gets an anonymous flow-pinned identity, and
  // lands in the tenant's IVR over WebRTC — no account. (Served by the API
  // at /sdk/voice.js.)
  {
    ...shared,
    entryPoints: ["src/guest-entry.ts"],
    outfile: "voice.js",
    format: "iife",
  },
];

if (watch) {
  for (const b of builds) {
    const ctx = await esbuild.context(b);
    await ctx.watch();
    console.log(`watching ${b.outfile}`);
  }
} else {
  for (const b of builds) {
    await esbuild.build(b);
    console.log(`built ${b.outfile}`);
  }
}
