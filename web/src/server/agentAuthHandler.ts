import {
  AGENT_API_KEY_PREFIX,
  AGENT_AUTH_HEADER,
  AGENT_WEB_SESSION_TTL_SECONDS,
  buildAgentSessionSetCookie,
  clearAgentSessionSetCookie,
  isAgentSessionExpired,
  readAgentSessionCookie,
  toPublicAgentSession,
  type AgentSessionCookiePayload
} from "../lib/agentSession";
import { AGENT_SESSION_COOKIE } from "../lib/sessionCookie";

interface ConnectErrorPayload {
  code?: string;
  message?: string;
}

interface GetAuthStatusJson {
  accounts?: string[];
  subjectId?: string;
}

interface ApiKeyJson {
  expiresAt?: string;
  id?: string;
  purpose?: string;
  revokedAt?: string;
}

interface ListApiKeysJson {
  keys?: ApiKeyJson[];
}

interface CreateApiKeyJson {
  key?: ApiKeyJson;
  secret?: string;
}

class ConnectCallError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function callConnectJson<T>(
  fetchFn: typeof fetch,
  apiBaseUrl: string,
  methodPath: string,
  token: string,
  body: Record<string, unknown>
): Promise<T> {
  const response = await fetchFn(`${apiBaseUrl.replace(/\/+$/, "")}${methodPath}`, {
    method: "POST",
    redirect: "follow",
    headers: {
      Authorization: `Bearer ${token}`,
      "Connect-Protocol-Version": "1",
      "Content-Type": "application/json"
    },
    body: JSON.stringify(body)
  });
  const text = await response.text();
  let parsed: unknown = {};
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { message: text };
    }
  }
  if (!response.ok) {
    const err = parsed as ConnectErrorPayload;
    throw new ConnectCallError(
      response.status,
      typeof err.code === "string" ? err.code : "unknown",
      typeof err.message === "string" && err.message ? err.message : `API request failed (${response.status})`
    );
  }
  return parsed as T;
}

function jsonNoStore(
  body: Record<string, unknown>,
  status = 200,
  setCookie?: string
): Response {
  const headers = new Headers({
    "Cache-Control": "no-store",
    "Content-Type": "application/json"
  });
  if (setCookie) {
    headers.append("Set-Cookie", setCookie);
  }
  return new Response(JSON.stringify(body), { status, headers });
}

function isTrustedSameOriginPost(request: Request): boolean {
  const requestOrigin = new URL(request.url).origin;
  const origin = request.headers.get("origin");
  if (origin && origin !== requestOrigin) {
    return false;
  }
  const fetchSite = request.headers.get("sec-fetch-site");
  if (fetchSite && fetchSite !== "same-origin" && fetchSite !== "none") {
    return false;
  }
  if (!origin && request.headers.get(AGENT_AUTH_HEADER) !== "1") {
    return false;
  }
  return true;
}

function findReusableWebKey(keys: ApiKeyJson[] | undefined, nowMs: number): ApiKeyJson | null {
  if (!keys || keys.length !== 1) {
    return null;
  }
  const [only] = keys;
  if (
    !only ||
    !only.id ||
    only.purpose !== "web" ||
    Boolean(only.revokedAt) ||
    !only.expiresAt ||
    isAgentSessionExpired(only.expiresAt, nowMs)
  ) {
    return null;
  }
  return only;
}

// handleAgentAuthRequest serves the Worker's same-origin agent web session
// endpoints (`/auth/agent/login`, `/auth/agent/token`, `/auth/agent/logout`).
// Long-lived agent API keys are exchanged for a short-lived, revocable child
// key (`purpose: "web"`) stored in an HttpOnly cookie so permanent keys never
// sit in browser storage.
export async function handleAgentAuthRequest(
  request: Request,
  apiBaseUrl: string,
  fetchFn: typeof fetch = fetch,
  now: () => number = Date.now
): Promise<Response | null> {
  const url = new URL(request.url);
  if (
    url.pathname !== "/auth/agent/login" &&
    url.pathname !== "/auth/agent/token" &&
    url.pathname !== "/auth/agent/logout"
  ) {
    return null;
  }

  if (request.method !== "POST") {
    return jsonNoStore({ error: "Method not allowed" }, 405);
  }
  if (!isTrustedSameOriginPost(request)) {
    return jsonNoStore({ error: "Cross-origin agent auth request rejected" }, 403);
  }

  const secure = url.protocol === "https:";
  const nowMs = now();

  if (url.pathname === "/auth/agent/token") {
    const cookieHeader = request.headers.get("cookie") ?? "";
    const session = readAgentSessionCookie(cookieHeader, nowMs);
    if (!session) {
      const hadCookie = cookieHeader.includes(`${AGENT_SESSION_COOKIE}=`);
      return jsonNoStore(
        { error: "No active agent session" },
        401,
        hadCookie ? clearAgentSessionSetCookie(secure) : undefined
      );
    }
    return jsonNoStore({
      session: toPublicAgentSession(session),
      token: session.token
    });
  }

  if (url.pathname === "/auth/agent/logout") {
    const session = readAgentSessionCookie(request.headers.get("cookie") ?? "", nowMs);
    if (session?.managed && session.keyId && apiBaseUrl) {
      try {
        await callConnectJson(
          fetchFn,
          apiBaseUrl,
          "/gitslice.core.v1.AuthService/RevokeApiKey",
          session.token,
          { keyId: session.keyId }
        );
      } catch {
        // Best-effort revocation: always clear the browser cookie even if the
        // key was already revoked or expired on the server.
      }
    }
    return jsonNoStore({ ok: true }, 200, clearAgentSessionSetCookie(secure));
  }

  // POST /auth/agent/login
  if (!apiBaseUrl) {
    return jsonNoStore({ error: "API base URL is not configured" }, 503);
  }

  let rawBody: { apiKey?: unknown };
  try {
    rawBody = (await request.json()) as { apiKey?: unknown };
  } catch {
    return jsonNoStore({ error: "Invalid JSON request body" }, 400);
  }

  const apiKey = typeof rawBody.apiKey === "string" ? rawBody.apiKey.trim() : "";
  if (!apiKey || !apiKey.startsWith(AGENT_API_KEY_PREFIX) || /\s/.test(apiKey)) {
    return jsonNoStore(
      { error: "Enter a valid Gitslice API key starting with gsk_." },
      400
    );
  }

  let statusResp: GetAuthStatusJson;
  try {
    statusResp = await callConnectJson<GetAuthStatusJson>(
      fetchFn,
      apiBaseUrl,
      "/gitslice.core.v1.AuthService/GetAuthStatus",
      apiKey,
      {}
    );
  } catch (err) {
    if (err instanceof ConnectCallError && (err.status === 401 || err.code === "unauthenticated")) {
      return jsonNoStore({ error: "API key was rejected (invalid, expired, or revoked)." }, 401);
    }
    return jsonNoStore(
      { error: err instanceof Error ? err.message : "Could not verify API key" },
      502
    );
  }

  const subjectId = (statusResp.subjectId ?? "").trim();
  const account = statusResp.accounts?.[0]?.trim() ?? "";
  if (!subjectId) {
    return jsonNoStore({ error: "Server did not return a subject for this API key." }, 502);
  }

  // If the presented key is already a dedicated expiring web-session key (for
  // example minted by `gs auth web-login`), use it directly instead of minting
  // a redundant grandchild key.
  let reusableWebKey: ApiKeyJson | null = null;
  try {
    const listed = await callConnectJson<ListApiKeysJson>(
      fetchFn,
      apiBaseUrl,
      "/gitslice.core.v1.AuthService/ListApiKeys",
      apiKey,
      {}
    );
    reusableWebKey = findReusableWebKey(listed.keys, nowMs);
  } catch {
    // Fall through to minting a short-lived child key.
  }

  let payload: AgentSessionCookiePayload;
  if (reusableWebKey) {
    payload = {
      token: apiKey,
      keyId: reusableWebKey.id,
      managed: true,
      subjectId,
      account,
      expiresAt: reusableWebKey.expiresAt
    };
  } else {
    let created: CreateApiKeyJson;
    try {
      created = await callConnectJson<CreateApiKeyJson>(
        fetchFn,
        apiBaseUrl,
        "/gitslice.core.v1.AuthService/CreateApiKey",
        apiKey,
        {
          name: "web-session",
          purpose: "web",
          expiresInSeconds: AGENT_WEB_SESSION_TTL_SECONDS
        }
      );
    } catch (err) {
      // If the parent key itself expires sooner than 24h, omit expiresInSeconds
      // so the server inherits the parent's expiry ceiling automatically.
      const parentShorter =
        err instanceof ConnectCallError &&
        /exceeds parent key expiry/i.test(err.message);
      if (parentShorter) {
        try {
          created = await callConnectJson<CreateApiKeyJson>(
            fetchFn,
            apiBaseUrl,
            "/gitslice.core.v1.AuthService/CreateApiKey",
            apiKey,
            {
              name: "web-session",
              purpose: "web"
            }
          );
        } catch (retryErr) {
          return jsonNoStore(
            { error: retryErr instanceof Error ? retryErr.message : "Could not create web session key" },
            retryErr instanceof ConnectCallError && retryErr.status >= 400 && retryErr.status < 500
              ? retryErr.status
              : 502
          );
        }
      } else {
        const status =
          err instanceof ConnectCallError && err.status >= 400 && err.status < 500
            ? err.status
            : 502;
        return jsonNoStore(
          { error: err instanceof Error ? err.message : "Could not create web session key" },
          status
        );
      }
    }

    if (!created.secret || !created.key?.id) {
      return jsonNoStore({ error: "Server did not return a web session credential." }, 502);
    }
    payload = {
      token: created.secret,
      keyId: created.key.id,
      managed: true,
      subjectId,
      account,
      expiresAt: created.key.expiresAt
    };
  }

  return jsonNoStore(
    {
      session: toPublicAgentSession(payload),
      token: payload.token
    },
    200,
    buildAgentSessionSetCookie(payload, secure, nowMs)
  );
}
