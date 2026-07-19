// First-party reverse proxy for PostHog ingestion.
//
// The web app points posthog-js at `<origin>/ingest` (VITE_POSTHOG_HOST) so that
// analytics requests are same-origin and survive tracker/ad blockers. This
// module forwards those `/ingest/*` requests to PostHog's ingestion and asset
// hosts. It runs in the Cloudflare Worker request path (see `src/server.ts`)
// ahead of the TanStack Start handler.
//
// Contract mirrors PostHog's documented reverse-proxy behaviour:
//   /ingest/static/*  -> <assets host>/static/*   (the posthog-js bundle, etc.)
//   /ingest/*         -> <ingestion host>/*        (events, /decide, /flags, …)
//
// Region defaults to US. For an EU project, override the two hosts below via
// build-time env (VITE_POSTHOG_UPSTREAM_HOST / VITE_POSTHOG_UPSTREAM_ASSETS_HOST),
// e.g. "eu.i.posthog.com" / "eu-assets.i.posthog.com".

const INGEST_PREFIX = "/ingest";

const env = import.meta.env as Record<string, string | undefined>;

const UPSTREAM_HOST = env.VITE_POSTHOG_UPSTREAM_HOST || "us.i.posthog.com";
const UPSTREAM_ASSETS_HOST =
  env.VITE_POSTHOG_UPSTREAM_ASSETS_HOST || "us-assets.i.posthog.com";

/**
 * Handle a `/ingest/*` request by proxying it to PostHog. Returns a `Response`
 * when the request targets the ingest prefix, or `undefined` to let the caller
 * fall through to the normal app handler.
 */
export async function handleIngestProxy(
  request: Request,
): Promise<Response | undefined> {
  const url = new URL(request.url);

  if (
    url.pathname !== INGEST_PREFIX &&
    !url.pathname.startsWith(`${INGEST_PREFIX}/`)
  ) {
    return undefined;
  }

  // Strip the `/ingest` prefix; everything after it is the real PostHog path.
  const upstreamPath = url.pathname.slice(INGEST_PREFIX.length) || "/";
  const isStatic = upstreamPath.startsWith("/static/");
  const upstreamHost = isStatic ? UPSTREAM_ASSETS_HOST : UPSTREAM_HOST;
  const targetUrl = `https://${upstreamHost}${upstreamPath}${url.search}`;

  // Rebuild the request against the target URL explicitly (rather than cloning
  // via `new Request(url, request)`, whose init-from-Request semantics vary
  // across runtimes). Don't leak first-party app cookies (Clerk session, etc.)
  // to PostHog. The outbound Host header is derived from the target URL by the
  // runtime (`Host` is a forbidden header that cannot be set manually).
  const headers = new Headers(request.headers);
  headers.delete("cookie");

  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  const proxyRequest = new Request(targetUrl, {
    method: request.method,
    headers,
    body: hasBody ? request.body : undefined,
    redirect: "manual",
    // Streaming an uploaded body requires the half-duplex opt-in.
    ...(hasBody ? { duplex: "half" } : {}),
  } as RequestInit);

  const upstreamResponse = await fetch(proxyRequest);

  if (!isStatic) {
    return upstreamResponse;
  }

  // Cache the static assets (the posthog-js bundle is content-hashed) so we
  // aren't proxying the same bytes on every page load.
  const response = new Response(upstreamResponse.body, upstreamResponse);
  response.headers.set("Cache-Control", "public, max-age=86400");
  return response;
}
