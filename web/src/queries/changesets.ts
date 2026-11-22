import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

import type { ListChangesetsResponse, SliceRef } from "../api/types";
import type { ApiClient } from "../api/useApi";
import { changesetsListRequest, type StatusTab } from "../routes/changesets-list/filters";

export const changesetsListKeyPrefix = (slice: SliceRef) =>
  ["changesets", slice.account ?? "", slice.slice ?? ""] as const;

export interface DependentChangeset {
  id: string;
  title: string;
}

export const changesetKey = (changesetId: string) => ["changeset", changesetId] as const;
export const changesetFileDiffKey = (
  changesetId: string,
  fromPatchset: string,
  toPatchset: string,
  path: string
) => ["changesetFileDiff", changesetId, fromPatchset, toPatchset, path] as const;

export function changesetQuery(api: Pick<ApiClient, "getChangeset">, changesetId: string) {
  return queryOptions({
    queryKey: changesetKey(changesetId),
    queryFn: () => api.getChangeset({ changesetId })
  });
}

// One comparison of a changeset's patchsets (from the recorded base when
// fromPatchset is empty), as the unified diff text.
export function changesetDiffQuery(
  api: Pick<ApiClient, "diffChangeset">,
  changesetId: string,
  fromPatchset: string,
  toPatchset: string
) {
  return queryOptions({
    queryKey: ["changesetDiff", changesetId, fromPatchset, toPatchset] as const,
    queryFn: () =>
      api.diffChangeset({
        changesetId,
        fromPatchset: fromPatchset || undefined,
        toPatchset: toPatchset || undefined
      })
  });
}

// The changesets stacked directly on one: the slice's recent changesets whose
// parent it is. Only their ids and titles are kept. The list it is read from
// is 200 changesets with their paths and descriptions (about 400 KB); cached
// whole, the server render put all of it into every changeset page's HTML to
// show what is usually no rows at all.
export function dependentChangesetsQuery(
  api: Pick<ApiClient, "listChangesets">,
  authoringSlice: SliceRef | undefined,
  changesetId: string
) {
  return queryOptions({
    queryKey: ["changesetDependents", authoringSlice?.account ?? "", authoringSlice?.slice ?? "", changesetId] as const,
    queryFn: async (): Promise<DependentChangeset[]> => {
      const listed = await api.listChangesets({ authoringSlice, limit: 200, summary: true });
      return (listed.changesets ?? [])
        .filter((candidate) => candidate.parentChangesetId === changesetId && candidate.id)
        .map((candidate) => ({ id: candidate.id as string, title: candidate.title ?? "" }));
    }
  });
}

// The slice's changesets list, one status group at a time, page after page.
// Shared by the page and the server render that prefetches its first page.
export function changesetsListQuery(
  api: Pick<ApiClient, "listChangesets">,
  slice: SliceRef,
  tab: StatusTab,
  typed: string
) {
  return infiniteQueryOptions({
    queryKey: [...changesetsListKeyPrefix(slice), tab, typed.trim()] as const,
    initialPageParam: "",
    queryFn: ({ pageParam }): Promise<ListChangesetsResponse> =>
      api.listChangesets(changesetsListRequest(slice, tab, typed, pageParam)),
    getNextPageParam: (last: ListChangesetsResponse) => last.nextPageToken || undefined
  });
}
