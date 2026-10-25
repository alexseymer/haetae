/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    sourcemap: false,
    assetsDir: "assets",
  },
  server: {
    proxy: {
      "/api": {
        target: "https://localhost:8080",
        secure: false,
      },
      "/healthz": {
        target: "https://localhost:8080",
        secure: false,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
});
