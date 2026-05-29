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
    // In dev (`npm run dev`) the Vue console runs on Vite's port, but
    // the api client uses relative URLs so it can ride on the embedded
    // mount in prod. Proxy the api paths to the Go binary on :8085 so
    // both modes Just Work. Override TRING_DEV_API_URL if your api
    // listens on a non-default host/port.
    proxy: {
      "/v1": process.env.TRING_DEV_API_URL || "http://localhost:8085",
      "/fs": process.env.TRING_DEV_API_URL || "http://localhost:8085",
      "/healthz": process.env.TRING_DEV_API_URL || "http://localhost:8085",
    },
  },
});
