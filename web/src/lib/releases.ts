// Release downloads are addressed through gitslice.io, so install.sh and the
// docs never name the store that holds the bytes. Assets currently live on
// the GitHub mirror's releases. Moving them later (for example to R2) only
// changes releasesBase. Redirects are temporary (302) for the same reason.
export const releasesBase = "https://github.com/gitslice-io/gitslice/releases";

// Tags and asset names: letters, digits, '.', '_', '+' and '-', starting with
// a letter or digit. This keeps redirects inside releasesBase (no '/' or '..').
const segment = /^[A-Za-z0-9][A-Za-z0-9._+-]*$/;

export function releaseRedirectTarget(request: Request): string | null {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return null;
  }
  const { pathname } = new URL(request.url);
  if (pathname !== "/releases" && !pathname.startsWith("/releases/")) {
    return null;
  }
  const parts = pathname.split("/").filter(Boolean).slice(1);
  if (parts.length === 0) {
    return releasesBase;
  }
  if (parts.length === 1 && parts[0] === "latest") {
    return `${releasesBase}/latest`;
  }
  if (parts.length === 3 && parts[0] === "latest" && parts[1] === "download" && segment.test(parts[2])) {
    return `${releasesBase}/latest/download/${parts[2]}`;
  }
  if (parts.length === 2 && parts[0] === "tag" && segment.test(parts[1])) {
    return `${releasesBase}/tag/${parts[1]}`;
  }
  if (parts.length === 3 && parts[0] === "download" && segment.test(parts[1]) && segment.test(parts[2])) {
    return `${releasesBase}/download/${parts[1]}/${parts[2]}`;
  }
  return null;
}

export function releaseRedirect(request: Request): Response | null {
  const target = releaseRedirectTarget(request);
  if (!target) {
    return null;
  }
  return new Response(null, {
    status: 302,
    headers: { Location: target, "Cache-Control": "public, max-age=60" }
  });
}
