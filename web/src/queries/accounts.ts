import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

import type { ListSlicesResponse } from "../api/types";
import type { ApiClient } from "../api/useApi";
import type { SliceSort } from "../lib/sliceOrder";

export const ACCOUNT_SLICES_PAGE_SIZE = 50;

export const accountProfileKey = (account: string) => ["accountProfile", account] as const;

// An account's profile. Written by the account page's profile editor and read
// by the SSR loader, so what is cached under the key is always the profile.
export function accountProfileQuery(api: Pick<ApiClient, "getAccountProfile">, account: string) {
  return queryOptions({
    queryKey: accountProfileKey(account),
    queryFn: () => api.getAccountProfile({ account })
  });
}

// The account's slices, a page at a time. The role and sort order are part of
// the key, so a member's list (private slices included) and each server sort
// order stay isolated in the cache.
export function accountSlicesQuery(
  api: Pick<ApiClient, "listSlices">,
  account: string,
  role: string,
  sort: SliceSort = "name"
) {
  return infiniteQueryOptions({
    queryKey: ["accountSlices", account, role, sort] as const,
    initialPageParam: "",
    queryFn: ({ pageParam }): Promise<ListSlicesResponse> =>
      api.listSlices({ account, cursor: pageParam, pageSize: ACCOUNT_SLICES_PAGE_SIZE, sort }),
    getNextPageParam: (last: ListSlicesResponse) => last.nextCursor || undefined
  });
}
