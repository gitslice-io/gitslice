// Proxy Streamable HTTP MCP requests (`/mcp`) from the public web origin to the
// Go API server's `/mcp` endpoint without touching the React SSR handler.

const FORWARDED_REQUEST_HEADERS = [
  "authorization",
  "content-type",
  "accept",
  "mcp-protocol-version",
  "x-gitslice-slice",
  "origin",
  "access-control-request-method",
  "access-control-request-headers"
];

export function mcpProxyTarget(request: Request, apiBaseUrl: string): string | undefined {
  const url = new URL(request.url);
  if (url.pathname !== "/mcp" && url.pathname !== "/mcp/") {
    return undefined;
  }
  const trimmedBase = apiBaseUrl.trim().replace(/\/+$/, "");
  if (!trimmedBase) {
    return undefined;
  }
  return `${trimmedBase}/mcp${url.search}`;
}

export async function handleMCPProxyRequest(
  request: Request,
  apiBaseUrl: string,
  fetchFn: typeof fetch = fetch
): Promise<Response | undefined> {
  const target = mcpProxyTarget(request, apiBaseUrl);
  if (!target) {
    return undefined;
  }

  const headers = new Headers();
  for (const name of FORWARDED_REQUEST_HEADERS) {
    const val = request.headers.get(name);
    if (val !== null) {
      headers.set(name, val);
    }
  }

  const init: RequestInit & { duplex?: "half" } = {
    method: request.method,
    headers,
    redirect: "manual"
  };

  if (request.method !== "GET" && request.method !== "HEAD" && request.body !== null) {
    init.body = request.body;
    init.duplex = "half";
  }

  return fetchFn(target, init);
}
