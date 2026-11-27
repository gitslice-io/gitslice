import { auth } from "@clerk/tanstack-react-start/server";

import {
  readAgentSessionCookie,
  toPublicAgentSession,
  type AgentSessionPublic
} from "../lib/agentSession";
import { hasSessionCookie } from "../lib/sessionCookie";
import { createApiClient } from "./client";
import { defaultApiBaseUrl } from "./rpcError";
import type { ApiClient } from "./useApi";

async function readServerAgentCookie() {
  const { getRequestHeader } = await import("@tanstack/react-start/server");
  return readAgentSessionCookie(getRequestHeader("cookie") ?? "");
}

// getServerAgentSession returns public metadata for an active agent web session
// cookie during SSR, or null when unauthenticated or signed in via Clerk. Never
// returns the raw bearer token.
export async function getServerAgentSession(): Promise<AgentSessionPublic | null> {
  if ((await auth()).isAuthenticated) {
    return null;
  }
  const session = await readServerAgentCookie();
  return session ? toPublicAgentSession(session) : null;
}

export async function createServerApiClient(
  baseUrl = defaultApiBaseUrl
): Promise<ApiClient> {
  const authState = await auth();
  const agentSession = authState.isAuthenticated ? null : await readServerAgentCookie();

  return createApiClient({
    baseUrl,
    // connect-web calls fetch with `redirect: "error"`, which the Cloudflare
    // Workers runtime rejects outright ("follow"/"manual" only), failing every
    // SSR RPC. The API never redirects, so following is equivalent.
    fetch: (input, init) => fetch(input, { ...init, redirect: "follow" }),
    getToken: async () => {
      if (authState.isAuthenticated) {
        return authState.getToken();
      }
      return agentSession?.token ?? null;
    }
  });
}

// isAnonymousRequest says the request being rendered has no session at all:
// the server did not authenticate it and it carries no cookie of one. Only for
// such a request is "not found" a settled answer to send as a 404; for anyone
// who may be signed in (a session the server could not read, say) the page
// decides in the browser, as it always did.
export async function isAnonymousRequest(): Promise<boolean> {
  if (await isServerSignedIn()) {
    return false;
  }
  const { getRequestHeader } = await import("@tanstack/react-start/server");
  return !hasSessionCookie(getRequestHeader("cookie") ?? "");
}

// isServerSignedIn says whether the request being rendered is signed in (via
// Clerk or an agent web session cookie), so the server render can prefetch what
// only a signed-in viewer needs.
export async function isServerSignedIn(): Promise<boolean> {
  if ((await auth()).isAuthenticated) {
    return true;
  }
  return (await readServerAgentCookie()) !== null;
}
