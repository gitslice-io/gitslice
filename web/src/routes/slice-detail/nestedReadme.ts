import { useQuery, useQueryClient } from "@tanstack/react-query";

import type { SliceRef, TreeEntry } from "../../api/types";
import { useApi } from "../../api/useApi";
import { listDirectoryAll, normalizeRepositoryPath } from "../../components/source/sourceUtils";
import { sliceDirectoryQueryKey } from "../../lib/sliceQueryKeys";
import { findReadmeEntry } from "./readme";

// How many single-folder levels a folder page looks down for a README.
export const NESTED_README_MAX_DEPTH = 8;

// onlyDirectory is the one entry of a folder that holds a single folder and
// nothing else.
export function onlyDirectory(entries: TreeEntry[]): TreeEntry | undefined {
  return entries.length === 1 && entries[0].kind === "ENTRY_KIND_DIRECTORY" ? entries[0] : undefined;
}

// findNestedReadme is the README a folder page shows: the folder's own, or,
// when the folder holds only one folder, that folder's, and so on down, as
// long as each level is a single folder (an account's slice that is one
// project at /account/project/). It stops where the tree branches.
export async function findNestedReadme(
  entries: TreeEntry[],
  list: (path: string) => Promise<TreeEntry[]>
): Promise<TreeEntry | undefined> {
  let current = entries;
  for (let depth = 0; ; depth++) {
    const readme = findReadmeEntry(current);
    if (readme) {
      return readme;
    }
    const only = onlyDirectory(current);
    if (!only || depth >= NESTED_README_MAX_DEPTH) {
      return undefined;
    }
    current = await list(normalizeRepositoryPath(only.path ?? ""));
  }
}

// nestedReadmeQueryKey caches findNestedReadme's answer for a folder whose
// only entry is the folder at onlyPath; the page's server render fills it.
export function nestedReadmeQueryKey(sliceRef: SliceRef | undefined, commitId: string, onlyPath: string) {
  return ["sliceNestedReadme", sliceRef?.account ?? "", sliceRef?.slice ?? "", commitId, onlyPath] as const;
}

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
        queryClient.fetchQuery({
          queryKey: sliceDirectoryQueryKey(sliceRef, commitId, path),
          queryFn: () => listDirectoryAll(api, { commitId, path, slice: sliceRef })
        })
      )) ?? null,
    // A commit's tree never changes.
    staleTime: Number.POSITIVE_INFINITY
  });
  if (direct) {
    return { loading: false, readme: direct };
  }
  return { loading: Boolean(only) && query.isPending, readme: query.data ?? undefined };
}
