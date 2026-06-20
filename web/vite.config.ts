import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "");
  // Optional dev-only proxy: set VITE_DEV_API_PROXY to a backend origin (e.g. a
  // staging API) to forward RPC calls through the dev server, avoiding CORS.
  // Leave VITE_API_BASE_URL empty so the client calls the same origin.
  const apiProxyTarget = env.VITE_DEV_API_PROXY;

  return {
    plugins: [react()],
    server: apiProxyTarget
      ? {
          proxy: {
            "/gitslice.core.v1.": {
              target: apiProxyTarget,
              changeOrigin: true,
              secure: true
            }
          }
        }
      : undefined
  };
});
