import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Tests run against a plain React + jsdom setup. We deliberately do NOT load the
// TanStack Start / Nitro plugins from vite.config.ts here: those wire up the SSR
// server build and hang vitest's workers (worker start-up timeouts). Unit tests
// only exercise React components, so the bare react() plugin is all they need.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom"
  }
});
