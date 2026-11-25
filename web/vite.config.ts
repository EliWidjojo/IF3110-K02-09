import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Build diletakkan di web/dist lalu di-embed oleh package Go `web`.
// Saat development, /api diteruskan ke backend Go agar cookie session tetap same-origin.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, assetsDir: "assets", sourcemap: false },
  server: {
    port: 5173,
    proxy: { "/api": { target: "http://localhost:8080", changeOrigin: false } },
  },
  test: { environment: "jsdom", globals: false },
});
