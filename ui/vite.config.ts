import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    // Off for the shipped bundle: 2.3MB of maps go into the container image
    // and nothing there consumes them. Build locally with `--sourcemap` when
    // debugging a deployed bundle.
    sourcemap: false,
  },
  server: {
    port: 5173,
    // Local dev proxies to a port-forwarded ui-backend; in-cluster the nginx
    // config does this instead, so no API host is ever baked into the bundle.
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
