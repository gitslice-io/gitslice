/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_CLERK_PUBLISHABLE_KEY?: string;
  // Dev/testing only: when "1"/"true", enables a localStorage/`?__dev_token=`
  // bearer-token bypass of Clerk (see src/auth/devAuth.ts). Never set for the
  // production build.
  readonly VITE_ENABLE_DEV_AUTH?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
