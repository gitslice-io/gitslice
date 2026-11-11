/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_CLERK_AUTHORIZED_PARTIES?: string;
  readonly VITE_CLERK_PUBLISHABLE_KEY?: string;
  // Product analytics (optional). Unset ⇒ posthog-js stays a no-op.
  readonly VITE_POSTHOG_KEY?: string;
  readonly VITE_POSTHOG_HOST?: string;
  // Deployment environment tag; set per build by deploy.sh (staging|production).
  readonly VITE_POSTHOG_ENV?: string;
  // /ingest reverse-proxy upstreams (optional; default to US region hosts).
  // Override for an EU project: "eu.i.posthog.com" / "eu-assets.i.posthog.com".
  readonly VITE_POSTHOG_UPSTREAM_HOST?: string;
  readonly VITE_POSTHOG_UPSTREAM_ASSETS_HOST?: string;
}

// Set per build in vite.config.ts; keys the edge page cache (lib/pageCache).
declare const __BUILD_ID__: string | undefined;

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
