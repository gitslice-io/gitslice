import { useQuery, useQueryClient } from "@tanstack/react-query";

import type { SliceRef, TreeEntry } from "../../api/types";
import { useApi } from "../../api/useApi";
import {
  findNestedReadme,
  findReadmeEntry,
  NESTED_README_MAX_DEPTH,
  onlyDirectory
} from "../../lib/readme";
import { normalizeRepositoryPath } from "../../lib/repoPaths";
import { nestedReadmeQueryKey } from "../../queries/repository";
import { sliceDirectoryQuery } from "../../queries/slices";

export { findNestedReadme, NESTED_README_MAX_DEPTH, nestedReadmeQueryKey, onlyDirectory };

// useFolderReadme finds a folder's README as findNestedReadme does. The
// listings it reads are the ones the folder pages use, so opening the folder
// it went through is instant.
export function useFolderReadme(
  commitId: string,
  sliceRef: SliceRef | undefined,
  entries: TreeEntry[]
): { loading: boolean; readme: TreeEntry | undefined } {
  const api = useApi();
  const queryClient = useQueryClient();
  const direct = findReadmeEntry(entries);
  const only = direct ? undefined : onlyDirectory(entries);
  const onlyPath = only ? normalizeRepositoryPath(only.path ?? "") : "";
  const query = useQuery({
    enabled: Boolean(only && commitId && sliceRef?.account && sliceRef?.slice),
    queryKey: nestedReadmeQueryKey(sliceRef, commitId, onlyPath),
    queryFn: async () =>
      (await findNestedReadme(entries, (path) =>
        queryClient.fetchQuery(sliceDirectoryQuery(api, sliceRef, commitId, path))
      )) ?? null,
    // A commit's tree never changes.
    staleTime: Number.POSITIVE_INFINITY
  });
  if (direct) {
    return { loading: false, readme: direct };
  }
  return { loading: Boolean(only) && query.isPending, readme: query.data ?? undefined };
}
