import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";

export default defineConfig({
  plugins: [react()],
  base: "/",
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  build: {
    outDir: "dist",
    chunkSizeWarningLimit: 1600,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8443",
      "/ws": { target: "http://localhost:8443", ws: true },
      "/metrics": "http://localhost:8443",
    },
  },
});
