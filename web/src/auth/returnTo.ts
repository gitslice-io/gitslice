// Where to send the user after they sign in. RequireAuth and "Sign in" prompts
// remember the page the user was on; LoginPage sends them back once Clerk has
// a session. Only same-origin paths are accepted, so this cannot become an
// open redirect.
const RETURN_TO_STORAGE_KEY = "gitslice.returnTo";

export function safeReturnPath(path: string | null | undefined): string | null {
  if (!path || !path.startsWith("/") || path.startsWith("//") || path.startsWith("/\\")) {
    return null;
  }
  if (path === "/login" || path.startsWith("/login/") || path.startsWith("/login?")) {
    return null;
  }
  return path;
}

export function rememberReturnTo(path: string) {
  const safe = safeReturnPath(path);
  if (!safe || typeof window === "undefined") {
    return;
  }
  try {
    sessionStorage.setItem(RETURN_TO_STORAGE_KEY, safe);
  } catch {
    // Storage can be unavailable (private mode); fall back to the home page.
  }
}

export function currentPath() {
  if (typeof window === "undefined") {
    return "/";
  }
  return window.location.pathname + window.location.search + window.location.hash;
}

export function peekReturnTo(): string | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    return safeReturnPath(sessionStorage.getItem(RETURN_TO_STORAGE_KEY));
  } catch {
    return null;
  }
}

export function clearReturnTo() {
  if (typeof window === "undefined") {
    return;
  }
  try {
    sessionStorage.removeItem(RETURN_TO_STORAGE_KEY);
  } catch {
    // Nothing to clear.
  }
}
