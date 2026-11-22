import { infiniteQueryOptions } from "@tanstack/react-query";

import type { ListCommitsResponse, SliceRef } from "../api/types";
import type { ApiClient } from "../api/useApi";
import { GLOBAL_REF_NAME } from "../lib/globalRef";

export const sliceCommitsKey = (
  sliceId: string,
  commitId: string,
  selectedPath: string,
  sliceRef?: SliceRef
) => ["sliceCommits", sliceId, commitId, selectedPath, sliceRef?.account, sliceRef?.slice] as const;

export const sliceFilesKey = (slice: SliceRef, commitId: string, includedPathsKey: string) =>
  ["sliceFiles", slice.account, slice.slice, commitId, includedPathsKey] as const;

export const changesetFileVersionKey = (treeId: string, path: string, slice?: SliceRef) =>
  ["changesetFileVersion", treeId, path, slice?.account, slice?.slice] as const;

export const pathSuggestKey = (commitId: string, dirPath: string) =>
  ["pathSuggest", commitId, dirPath] as const;

// nestedReadmeQueryKey caches findNestedReadme's answer for a folder whose
// only entry is the folder at onlyPath; the page's server render fills it.
export function nestedReadmeQueryKey(sliceRef: SliceRef | undefined, commitId: string, onlyPath: string) {
  return ["sliceNestedReadme", sliceRef?.account ?? "", sliceRef?.slice ?? "", commitId, onlyPath] as const;
}

export function sliceCommitsInfiniteQuery(
  api: Pick<ApiClient, "listCommits">,
  sliceId: string,
  commitId: string,
  selectedPath: string,
  sliceRef: SliceRef | undefined,
  limit = 50
) {
  return infiniteQueryOptions({
    queryKey: sliceCommitsKey(sliceId, commitId, selectedPath, sliceRef),
    initialPageParam: "",
    queryFn: ({ pageParam }): Promise<ListCommitsResponse> =>
      api.listCommits({
        refName: GLOBAL_REF_NAME,
        slice: sliceRef,
        path: selectedPath || undefined,
        limit,
        ...(pageParam ? { pageToken: pageParam } : {})
      }),
    getNextPageParam: (last: ListCommitsResponse) => last.nextPageToken || undefined
  });
}
