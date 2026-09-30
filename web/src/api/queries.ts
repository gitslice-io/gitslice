import { queryOptions } from "@tanstack/react-query";

import type {
  Changeset,
  ListChangesetsResponse,
  Slice,
  SliceRef
} from "./types";
import type { ApiClient } from "./useApi";

// Query keys and fetchers shared by the SSR route loaders (routes/router.tsx)
// and the components that read the same data. A server prefetch only helps if
// it lands in exactly the cache entry the component reads, with exactly the
// shape it expects, so both sides build their queries from these factories.
// Callers add `enabled` themselves; it depends on auth state the factories do
// not know about.

export function authStatusQuery(api: ApiClient) {
  return queryOptions({
    queryKey: ["authStatus"],
    queryFn: () => api.getAuthStatus({})
  });
}

const SLICE_PAGE_SIZE = 100;

// Every slice in one account, following pagination to the end.
export function accountSlicesQuery(api: ApiClient, account: string) {
  return queryOptions({
    queryKey: ["slices", account],
    queryFn: async () => {
      const slices: Slice[] = [];
      let cursor = "";

      do {
        const response = await api.listSlices({
          account,
          cursor,
          pageSize: SLICE_PAGE_SIZE
        });

        slices.push(...(response.slices ?? []));
        cursor = response.nextCursor ?? "";
      } while (cursor);

      return slices;
    }
  });
}

// Agents the signed-in user co-owns. Failures are expected for accounts
// without agent support, so they are not retried.
export function ownedAgentsQuery(api: ApiClient) {
  return queryOptions({
    queryKey: ["ownedAgents"],
    queryFn: async () => (await api.listOwnedAgents({})).agents ?? [],
    retry: false
  });
}

export function pendingClaimsQuery(api: ApiClient) {
  return queryOptions({
    queryKey: ["pendingClaims"],
    queryFn: async () => (await api.listPendingClaims({})).claims ?? [],
    retry: false
  });
}

export function recentConversationsQuery(api: ApiClient) {
  return queryOptions({
    queryKey: ["recentConversations"],
    queryFn: async () => (await api.listConversations({})).conversations ?? []
  });
}

// ListChangesets returns every patchset with its full file-edit list. The
// changeset list page never reads patchsets, and they dominate the payload
// (a single import can carry thousands of edits), so the cached list drops
// them. That keeps the dehydrated SSR state, and the HTML, small.
export function sliceChangesetsQuery(
  api: ApiClient,
  account: string,
  slice: string
) {
  return queryOptions({
    queryKey: ["changesets", account, slice],
    queryFn: async (): Promise<ListChangesetsResponse> => {
      const response = await api.listChangesets({
        authoringSlice: { account, slice }
      });
      return {
        ...response,
        changesets: (response.changesets ?? []).map(withoutPatchsets)
      };
    }
  });
}

export type DependentCandidate = Pick<
  Changeset,
  "id" | "title" | "parentChangesetId"
>;

// The changeset page only needs enough of its sibling changesets to find the
// ones stacked on top of it.
export function dependentCandidatesQuery(
  api: ApiClient,
  authoringSlice: SliceRef | undefined
) {
  return queryOptions({
    queryKey: [
      "changesetsBySlice",
      authoringSlice?.account,
      authoringSlice?.slice
    ],
    queryFn: async (): Promise<{ changesets: DependentCandidate[] }> => {
      const response = await api.listChangesets({ authoringSlice, limit: 200 });
      return {
        changesets: (response.changesets ?? []).map(
          ({ id, title, parentChangesetId }) => ({
            id,
            title,
            parentChangesetId
          })
        )
      };
    }
  });
}

function withoutPatchsets(changeset: Changeset): Changeset {
  const { patchsets: _patchsets, ...rest } = changeset;
  return rest;
}
