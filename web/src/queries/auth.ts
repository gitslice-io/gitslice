import { queryOptions } from "@tanstack/react-query";

import type { SharedSlice } from "../api/types";
import type { ApiClient } from "../api/useApi";

// Who the viewer is, and what is shared with them. The SSR route loaders, the
// selection provider and the permission hooks read these through the same
// options, so a key and the shape cached under it are written once.

export const authStatusKey = ["authStatus"] as const;
export const sharedSlicesKey = ["sharedSlices"] as const;

export function authStatusQuery(api: Pick<ApiClient, "getAuthStatus">) {
  return queryOptions({
    queryKey: authStatusKey,
    queryFn: () => api.getAuthStatus({})
  });
}

// Slices other accounts shared with the viewer, with the role on each. A
// server that lacks the call, or a test client without it, shares nothing.
export function sharedSlicesQuery(api: Partial<Pick<ApiClient, "listSharedSlices">>) {
  return queryOptions({
    queryKey: sharedSlicesKey,
    queryFn: async (): Promise<SharedSlice[]> => {
      if (typeof api.listSharedSlices !== "function") {
        return [];
      }
      return (await api.listSharedSlices({})).slices ?? [];
    }
  });
}
