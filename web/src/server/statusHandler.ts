export interface ServiceHealthPayload {
  overall: "operational" | "degraded";
  checkedAt: string;
  api: {
    status: "operational" | "degraded";
    latencyMs: number | null;
    httpStatus: number | null;
  };
}

const DEFAULT_API_BASE = "https://api.gitslice.io";
const PROBE_TIMEOUT_MS = 5000;

export async function handleStatusRequest(
  request: Request,
  apiBaseUrl: string,
  fetchFn: typeof fetch = fetch
): Promise<Response | null> {
  const url = new URL(request.url);
  if (url.pathname !== "/api/status") {
    return null;
  }
  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response("Method Not Allowed", {
      status: 405,
      headers: { Allow: "GET, HEAD" }
    });
  }

  const base = (apiBaseUrl || DEFAULT_API_BASE).replace(/\/+$/, "");
  const readyzUrl = `${base}/readyz`;
  const started = Date.now();
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);

  let ok = false;
  let httpStatus: number | null = null;
  let latencyMs: number | null = null;
  try {
    const res = await fetchFn(readyzUrl, {
      method: "GET",
      cache: "no-store",
      signal: controller.signal
    });
    httpStatus = res.status;
    latencyMs = Math.max(1, Date.now() - started);
    ok = res.ok;
  } catch {
    ok = false;
  } finally {
    clearTimeout(timer);
  }

  const payload: ServiceHealthPayload = {
    overall: ok ? "operational" : "degraded",
    checkedAt: new Date().toISOString(),
    api: {
      status: ok ? "operational" : "degraded",
      latencyMs,
      httpStatus
    }
  };

  return new Response(request.method === "HEAD" ? null : JSON.stringify(payload), {
    status: 200,
    headers: {
      "Content-Type": "application/json; charset=utf-8",
      "Cache-Control": "no-store"
    }
  });
}
