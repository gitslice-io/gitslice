/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_CLERK_AUTHORIZED_PARTIES?: string;
  readonly VITE_CLERK_PUBLISHABLE_KEY?: string;
  // Product analytics (optional). Unset ⇒ posthog-js stays a no-op.
  readonly VITE_POSTHOG_KEY?: string;
  readonly VITE_POSTHOG_HOST?: string;
  // /ingest reverse-proxy upstreams (optional; default to US region hosts).
  // Override for an EU project: "eu.i.posthog.com" / "eu-assets.i.posthog.com".
  readonly VITE_POSTHOG_UPSTREAM_HOST?: string;
  readonly VITE_POSTHOG_UPSTREAM_ASSETS_HOST?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
