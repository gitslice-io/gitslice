import { useEffect, useState } from "react";

// useGitCloneOrigin returns the origin to use when building the git clone URL.
//
// The git smart-HTTP endpoint is served same-origin with the web app (the
// Cloudflare Worker reverse-proxies /git/* to the backend), so a deployed clone
// URL should use the live browser origin (e.g. https://gitslice.io) rather than
// a separately configured host. This returns `undefined` during SSR and the
// first client render — matching the server-rendered fallback so hydration does
// not mismatch — then, outside dev, the real origin once mounted. In dev it
// stays `undefined` so the configured VITE_GITSLICE_GIT_HTTP_BASE_URL is used
// (there the git server runs on its own port, not the Vite dev origin).
export function useGitCloneOrigin(): string | undefined {
  const [origin, setOrigin] = useState<string | undefined>(undefined);
  useEffect(() => {
    if (!import.meta.env.DEV) {
      setOrigin(window.location.origin);
    }
  }, []);
  return origin;
}
