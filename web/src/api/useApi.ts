import { useAuth } from "@clerk/tanstack-react-start";
import { useCallback, useEffect, useMemo } from "react";

import { afterPageLoad } from "../analytics/idle";
import type { GeneratedApi } from "./generated";
import { createLazyApiClient, loadApiClientModule } from "./lazyClient";
import { defaultApiBaseUrl } from "./rpcError";

// Every unary and server-streaming RPC of the browser-facing gRPC services,
// with requests and responses typed directly from the generated protobufs.
export type ApiClient = GeneratedApi;

export function useApi(baseUrl = defaultApiBaseUrl): ApiClient {
  const { getToken, isLoaded, isSignedIn } = useAuth();

  const getApiToken = useCallback(
    async () => (isLoaded && isSignedIn ? await getToken() : null),
    [getToken, isLoaded, isSignedIn]
  );

  // The client's code is not in the page's first script (./lazyClient). It
  // is fetched once the page has loaded, so it is there by the time a call
  // is made; a call made sooner fetches it itself.
  useEffect(() => {
    void afterPageLoad()
      .then(loadApiClientModule)
      .catch(() => undefined);
  }, []);

  return useMemo(
    () => createLazyApiClient({ getToken: getApiToken, baseUrl }),
    [baseUrl, getApiToken]
  );
}
