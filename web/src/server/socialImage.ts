// Link-preview images (/og/...) and account pictures (/avatars/...) are
// drawn by the API server and served from the site's own address, cached at
// Cloudflare's edge, so a crawler fetching a card, or a page listing many
// accounts, rarely reaches the API (and does not wake it).

const imagePrefixes = ["/og/", "/avatars/"];

type EdgeCache = {
  match(request: Request): Promise<Response | undefined>;
  put(request: Request, response: Response): Promise<void>;
};

export async function socialImageResponse(
  request: Request,
  apiBase: string,
  cache: EdgeCache | undefined = edgeCache()
): Promise<Response | null> {
  const url = new URL(request.url);
  if (!imagePrefixes.some((prefix) => url.pathname.startsWith(prefix)) || !apiBase) {
    return null;
  }
  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response("method not allowed", { status: 405 });
  }
  // One cache entry per image and version, whatever the method or headers.
  const key = new Request(url.toString(), { method: "GET" });
  const cached = await cache?.match(key);
  if (cached) {
    return request.method === "HEAD" ? new Response(null, cached) : cached;
  }
  const upstream = await fetch(`${apiBase.replace(/\/+$/, "")}${url.pathname}${url.search}`, {
    redirect: "follow"
  });
  const response = new Response(upstream.body, upstream);
  if (upstream.ok && cache) {
    await cache.put(key, response.clone());
  }
  return request.method === "HEAD" ? new Response(null, response) : response;
}

function edgeCache(): EdgeCache | undefined {
  const caches = (globalThis as { caches?: { default?: EdgeCache } }).caches;
  return caches?.default;
}
