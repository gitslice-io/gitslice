import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Tests run against a plain React setup. We deliberately do NOT load the
// TanStack Start / Nitro plugins from vite.config.ts here: those wire up the SSR
// server build and hang vitest's workers (worker start-up timeouts). Unit tests
// only exercise React components and plain modules, so the bare react() plugin
// is all they need.
//
// Component tests (*.test.tsx) and the few plain tests that touch the DOM run in
// jsdom; every other plain test runs in node, which starts much faster than a
// jsdom environment for each file (most of the suite's time on the 2-CPU CI
// host).
const domTests = ["src/**/*.test.tsx", "src/auth/returnTo.test.ts", "src/routes/router.test.ts"];

export default defineConfig({
  plugins: [react()],
  test: {
    projects: [
      {
        extends: true,
        test: { name: "dom", environment: "jsdom", include: domTests }
      },
      {
        extends: true,
        test: { name: "node", environment: "node", include: ["src/**/*.test.ts"], exclude: domTests }
      }
    ]
  }
});
