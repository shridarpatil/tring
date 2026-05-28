import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "node:path";

// Build output goes into the Go module's embed source dir so the API
// binary picks it up via //go:embed at compile time. The Tring API
// serves the SPA at /admin/* — base must match for asset URLs to
// resolve correctly when loaded via the embedded mount.
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  base: "/admin/",
  build: {
    outDir: resolve(__dirname, "../internal/webui/dist"),
    emptyOutDir: true,
  },
  server: {
    port: 5173,
  },
});
