import { auth } from "@clerk/tanstack-react-start/server";

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
