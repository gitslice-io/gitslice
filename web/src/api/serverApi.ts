import { auth } from "@clerk/tanstack-react-start/server";

import { createApiClient, defaultApiBaseUrl } from "./client";
import type { ApiClient } from "./useApi";

export async function createServerApiClient(
  baseUrl = defaultApiBaseUrl
): Promise<ApiClient> {
  const authState = await auth();

  return createApiClient({
    baseUrl,
    getToken: async () =>
      authState.isAuthenticated ? await authState.getToken() : null
  });
}
