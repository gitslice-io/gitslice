// Dev-only auth bypass for headless/automated testing.
//
// Entirely gated behind the VITE_ENABLE_DEV_AUTH build flag, which is NOT set for
// the production/Clerk build — so `devAuthEnabled` is a compile-time `false` and
// the whole path is tree-shaken out of the deployed bundle. When enabled, the app
// authenticates with a bearer token taken from localStorage (or a one-time
// `?__dev_token=` URL param), instead of Clerk. The token must still be a valid
// signed token the server accepts (e.g. a service token from
// `gs auth mint-service-token`), so this is a UI convenience, not a server bypass.

const DEV_TOKEN_KEY = "gitslice.dev.token";

const flag = import.meta.env.VITE_ENABLE_DEV_AUTH;
export const devAuthEnabled = flag === "1" || flag === "true";

export function getDevToken(): string | null {
  if (!devAuthEnabled) {
    return null;
  }
  try {
    const fromUrl = new URLSearchParams(window.location.search).get("__dev_token");
    if (fromUrl) {
      localStorage.setItem(DEV_TOKEN_KEY, fromUrl);
    }
    return localStorage.getItem(DEV_TOKEN_KEY);
  } catch {
    return null;
  }
}
