import { auth } from "@clerk/tanstack-react-start/server";

import { hasSessionCookie } from "../lib/sessionCookie";
import { createApiClient, defaultApiBaseUrl } from "./client";
import type { ApiClient } from "./useApi";

export async function createServerApiClient(
  baseUrl = defaultApiBaseUrl
): Promise<ApiClient> {
  const authState = await auth();

  return createApiClient({
    baseUrl,
    // connect-web calls fetch with `redirect: "error"`, which the Cloudflare
    // Workers runtime rejects outright ("follow"/"manual" only), failing every
    // SSR RPC. The API never redirects, so following is equivalent.
    fetch: (input, init) => fetch(input, { ...init, redirect: "follow" }),
    getToken: async () =>
      authState.isAuthenticated ? await authState.getToken() : null
  });
}

// isAnonymousRequest says the request being rendered has no session at all:
// the server did not authenticate it and it carries no cookie of one. Only for
// such a request is "not found" a settled answer to send as a 404; for anyone
// who may be signed in (a session the server could not read, say) the page
// decides in the browser, as it always did.
export async function isAnonymousRequest(): Promise<boolean> {
  if ((await auth()).isAuthenticated) {
    return false;
  }
  const { getRequestHeader } = await import("@tanstack/react-start/server");
  return !hasSessionCookie(getRequestHeader("cookie") ?? "");
}

// isServerSignedIn says whether the request being rendered is signed in, so
// the server render can prefetch what only a signed-in viewer needs.
export async function isServerSignedIn(): Promise<boolean> {
  return (await auth()).isAuthenticated;
}
