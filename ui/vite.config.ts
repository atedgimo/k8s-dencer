import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    sourcemap: true,
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
