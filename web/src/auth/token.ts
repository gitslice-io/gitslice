// Minted-token auth: an alternative to the interactive Clerk session, mirroring
// the CLI's `gs auth token` / service tokens. A token placed in localStorage (or
// passed once as a `?token=` query param) is sent as the API bearer and treated
// as an authenticated session, so headless tooling, CI dashboards, and
// token-based access work without the Clerk browser flow.

const STORAGE_KEY = "gitslice.token";

export function getMintedToken(): string | null {
  try {
    const value = window.localStorage.getItem(STORAGE_KEY);
    return value && value.trim() !== "" ? value : null;
  } catch {
    return null;
  }
}

export function setMintedToken(token: string): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, token.trim());
  } catch {
    // ignore storage failures (private mode, disabled storage)
  }
}

export function clearMintedToken(): void {
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // ignore
  }
}

export function hasMintedToken(): boolean {
  return getMintedToken() !== null;
}

// captureTokenFromUrl persists a `?token=` param into storage and strips it from
// the address bar so the secret is not left in history or shared links. Call once
// at startup before rendering.
export function captureTokenFromUrl(): void {
  try {
    const url = new URL(window.location.href);
    const token = url.searchParams.get("token");
    if (token && token.trim() !== "") {
      setMintedToken(token);
      url.searchParams.delete("token");
      window.history.replaceState(
        window.history.state,
        "",
        url.pathname + url.search + url.hash
      );
    }
  } catch {
    // ignore malformed URLs / missing history API
  }
}
