/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_CLERK_AUTHORIZED_PARTIES?: string;
  readonly VITE_CLERK_PUBLISHABLE_KEY?: string;
  // Product analytics (optional). Unset ⇒ posthog-js stays a no-op.
  readonly VITE_POSTHOG_KEY?: string;
  readonly VITE_POSTHOG_HOST?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
