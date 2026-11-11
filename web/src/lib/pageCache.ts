import { hasSessionCookie } from "./sessionCookie";
import { visitorBannerWanted } from "./visitorBanner";

// A short edge cache for the pages a visitor with no session reads from the
// API: a slice, a changeset, a list of changesets, an account. Rendering one
// costs several API round trips (about half a second); everyone with no
// session is served the same HTML, so the Worker keeps it for a moment.
//
// - Only a request that cannot be signed in is served from it or stored: no
//   session cookie, no Authorization header. Signed-in people always get a
//   fresh render.
// - A copy is fresh for PAGE_FRESH_MS. After that, and up to PAGE_STALE_MS,
//   it is still served at once while a new render replaces it in the
//   background. The browser then refetches the page's data itself (the
//   queries in the HTML are older than their stale time), so what a visitor
//   reads catches up a moment after the page appears.
// - The key holds the build, so a deploy never serves HTML that names the
//   previous build's scripts.
// - Nothing runs on a timer: a copy is only refreshed by a request for it.

export const PAGE_FRESH_MS = 30_000;
export const PAGE_STALE_MS = 5 * 60_000;

const STORED_AT_HEADER = "x-gitslice-page-stored-at";
// Shown on the response: "hit", "stale" (served, being refreshed) or "miss".
export const PAGE_CACHE_HEADER = "x-gitslice-page-cache";

const CACHEABLE_PATH = /^\/(?:slices\/[^/]+\/[^/]+|cs\/[^/]+|changesets|accounts\/[^/]+)\/?$/;

// pageCacheKey is the cache key for a request that may be served from the
// page cache, or undefined for one that must be rendered for itself.
export function pageCacheKey(request: Request, buildId: string): string | undefined {
  if (request.method !== "GET") {
    return undefined;
  }
  const url = new URL(request.url);
  if (!CACHEABLE_PATH.test(url.pathname)) {
    return undefined;
  }
  if (!(request.headers.get("accept") ?? "").includes("text/html")) {
    return undefined;
  }
  if (request.headers.has("authorization")) {
    return undefined;
  }
  const cookies = request.headers.get("cookie") ?? "";
  if (hasSessionCookie(cookies)) {
    return undefined;
  }
  const params = [...url.searchParams.entries()];
  // Clerk's own parameters (a handshake, a development session) are for this
  // browser alone.
  if (params.some(([name]) => name.startsWith("__clerk"))) {
    return undefined;
  }
  params.sort(([a, av], [b, bv]) => (a === b ? av.localeCompare(bv) : a.localeCompare(b)));
  const search = new URLSearchParams(params).toString();
  // The one thing the HTML differs by for visitors with no session.
  const banner = visitorBannerWanted(cookies) ? "1" : "0";
  return `${url.origin}/__page-cache/${encodeURIComponent(buildId)}/banner-${banner}${url.pathname}${search ? `?${search}` : ""}`;
}

// storablePage says whether a rendered response may be kept: a complete HTML
// page that sets no cookie.
export function storablePage(response: Response): boolean {
  return (
    response.status === 200 &&
    (response.headers.get("content-type") ?? "").includes("text/html") &&
    !response.headers.has("set-cookie")
  );
}

// The part of the Workers Cache API this uses.
export interface PageCacheStore {
  match(key: string): Promise<Response | undefined>;
  put(key: string, response: Response): Promise<void>;
}

export interface PageCacheOptions {
  buildId: string;
  cache: PageCacheStore | undefined;
  now?: () => number;
  render: (request: Request) => Promise<Response>;
  // Runs work after the response has been sent. Without it a stored copy is
  // only used while fresh, since nothing could refresh it.
  waitUntil?: (work: Promise<unknown>) => void;
}

function withCacheState(response: Response, state: string): Response {
  const headers = new Headers(response.headers);
  headers.delete(STORED_AT_HEADER);
  headers.set(PAGE_CACHE_HEADER, state);
  // The copy is the Worker's; the browser asks again each time.
  headers.set("cache-control", "private, no-cache");
  return new Response(response.body, { headers, status: response.status, statusText: response.statusText });
}

async function renderAndStore(request: Request, key: string, options: PageCacheOptions & { cache: PageCacheStore }) {
  const response = await options.render(request);
  if (!storablePage(response)) {
    return response;
  }
  const now = (options.now ?? Date.now)();
  const headers = new Headers(response.headers);
  headers.set(STORED_AT_HEADER, String(now));
  headers.set("cache-control", `public, s-maxage=${Math.ceil(PAGE_STALE_MS / 1000)}`);
  const body = await response.arrayBuffer();
  const store = options.cache
    .put(key, new Response(body, { headers, status: 200 }))
    .catch(() => undefined);
  if (options.waitUntil) {
    options.waitUntil(store);
  } else {
    await store;
  }
  return new Response(body, { headers: response.headers, status: 200, statusText: response.statusText });
}

// servePage answers a request from the page cache where it may, and renders
// it otherwise. Any failure of the cache itself falls back to rendering.
export async function servePage(request: Request, options: PageCacheOptions): Promise<Response> {
  const key = options.cache ? pageCacheKey(request, options.buildId) : undefined;
  const cache = options.cache;
  if (!key || !cache) {
    return options.render(request);
  }
  const withCache = { ...options, cache };
  let stored: Response | undefined;
  try {
    stored = await cache.match(key);
  } catch {
    return options.render(request);
  }
  if (stored) {
    const age = (options.now ?? Date.now)() - Number(stored.headers.get(STORED_AT_HEADER) ?? 0);
    if (age >= 0 && age < PAGE_FRESH_MS) {
      return withCacheState(stored, "hit");
    }
    if (age >= 0 && age < PAGE_STALE_MS && options.waitUntil) {
      options.waitUntil(renderAndStore(request, key, withCache).catch(() => undefined));
      return withCacheState(stored, "stale");
    }
  }
  return withCacheState(await renderAndStore(request, key, withCache), "miss");
}
