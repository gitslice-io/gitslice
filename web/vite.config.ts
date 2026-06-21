import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import { nitro } from "nitro/vite";

export default defineConfig({
  plugins: [
    tanstackStart({
      router: {
        entry: "routes/router",
        generatedRouteTree: "start-routeTree.gen.ts",
        quoteStyle: "double",
        routesDirectory: "start-routes",
        semicolons: true
      }
    }),
    react(),
    nitro({
      preset: "cloudflare-module",
      compatibilityDate: "2026-06-13",
      cloudflare: {
        deployConfig: false
      }
    })
  ],
  environments: {
    ssr: {
      build: {
        rollupOptions: {
          input: "./src/server.ts"
        }
      }
    }
  }
});
