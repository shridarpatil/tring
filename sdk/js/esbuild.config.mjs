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

const builds = [
  // IIFE — bare SDK. Exposes a global `Tring`; integrator wires it manually.
  {
    ...shared,
    outfile: "dist/tring.iife.js",
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
  // voice.js — the drop-in agent widget. Auto-binds <button data-tring-call>
  // and renders the floating call panel on DOM ready, reading config
  // from its own <script> tag's data-* attributes.
  {
    ...shared,
    entryPoints: ["src/widget-entry.ts"],
    outfile: "dist/voice.iife.js",
    format: "iife",
  },
  // support.js — the drop-in *guest* widget for customer-facing support
  // pages. Auto-renders a "Call support" launcher + full-screen call UI;
  // the visitor calls into the tenant's IVR over WebRTC with no account.
  {
    ...shared,
    entryPoints: ["src/guest-entry.ts"],
    outfile: "dist/support.iife.js",
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
