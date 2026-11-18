import { useQueries, useQuery } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";

import type { Changeset, Patchset } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import type { DiffViewerFileState } from "../../components/diff/DiffViewer";
import { changesetDiffQuery } from "../../queries/changesets";
import {
  changedPathsForDiff,
  diffFileForResponse,
  fileMetadataForPath
} from "./diffFiles";
import { sortedPatchsets } from "./patchsetUtils";

export const FULL_DIFF_PATH_LIMIT = 20;
export const EAGER_FILE_DIFF_COUNT = 10;

export interface UsePatchsetDiffComparisonOptions {
  api: ApiClient;
  changeset: Changeset | undefined;
  canonicalChangesetId: string;
  fromSearch?: string;
  toSearch?: string;
  focusFilePath?: string;
}

export function usePatchsetDiffComparison({
  api,
  changeset,
  canonicalChangesetId,
  fromSearch,
  toSearch,
  focusFilePath
}: UsePatchsetDiffComparisonOptions) {
  const patchsets: Patchset[] = useMemo(() => sortedPatchsets(changeset), [changeset]);
  const patchsetIds = useMemo(
    () =>
      new Set(
        patchsets
          .map((patchset) => patchset.id)
          .filter((id): id is string => Boolean(id))
      ),
    [patchsets]
  );

  const defaultToPatchset =
    changeset?.currentPatchsetId || patchsets[patchsets.length - 1]?.id || "";
  const fromPatchset =
    fromSearch && patchsetIds.has(fromSearch) ? fromSearch : "";
  const selectedToPatchset =
    toSearch && patchsetIds.has(toSearch) ? toSearch : defaultToPatchset;

  const selectedFromPatchset = useMemo(
    () => patchsets.find((patchset) => patchset.id === fromPatchset),
    [fromPatchset, patchsets]
  );
  const selectedToPatchsetMetadata = useMemo(
    () => patchsets.find((patchset) => patchset.id === selectedToPatchset),
    [patchsets, selectedToPatchset]
  );

  const changedPaths = useMemo(
    () => changedPathsForDiff(selectedFromPatchset, selectedToPatchsetMetadata),
    [selectedFromPatchset, selectedToPatchsetMetadata]
  );
  const changedPathSet = useMemo(() => new Set(changedPaths), [changedPaths]);

  const fileMetadataByPath = useMemo(
    () =>
      new Map(
        changedPaths.map((path) => [
          path,
          fileMetadataForPath(path, selectedFromPatchset, selectedToPatchsetMetadata)
        ])
      ),
    [changedPaths, selectedFromPatchset, selectedToPatchsetMetadata]
  );

  const usesLazyFileDiffs = changedPaths.length > FULL_DIFF_PATH_LIMIT;
  const comparisonKey = `${canonicalChangesetId}\0${fromPatchset}\0${selectedToPatchset}`;

  const [requestedFilePaths, setRequestedFilePaths] = useState<{
    comparisonKey: string;
    paths: Set<string>;
  }>(() => ({ comparisonKey: "", paths: new Set() }));

  const demandedFilePaths =
    requestedFilePaths.comparisonKey === comparisonKey
      ? requestedFilePaths.paths
      : undefined;

  const requestFileDiff = useCallback(
    (path: string) => {
      if (!usesLazyFileDiffs || !changedPathSet.has(path)) {
        return;
      }
      setRequestedFilePaths((current) => {
        const currentPaths =
          current.comparisonKey === comparisonKey
            ? current.paths
            : new Set<string>();
        if (currentPaths.has(path)) {
          return current;
        }
        const nextPaths = new Set(currentPaths);
        nextPaths.add(path);
        return { comparisonKey, paths: nextPaths };
      });
    },
    [changedPathSet, comparisonKey, usesLazyFileDiffs]
  );

  const diffQuery = useQuery({
    enabled: Boolean(changeset && canonicalChangesetId && !usesLazyFileDiffs),
    ...changesetDiffQuery(api, canonicalChangesetId, fromPatchset, selectedToPatchset)
  });

  const fileDiffQueries = useQueries({
    queries: usesLazyFileDiffs
      ? changedPaths.map((path, index) => ({
          enabled: Boolean(
            canonicalChangesetId &&
              (index < EAGER_FILE_DIFF_COUNT ||
                demandedFilePaths?.has(path) ||
                focusFilePath === path)
          ),
          queryKey: [
            "changesetFileDiff",
            canonicalChangesetId,
            fromPatchset,
            selectedToPatchset,
            path
          ],
          queryFn: async () => {
            const response = await api.diffChangeset({
              changesetId: canonicalChangesetId,
              fromPatchset: fromPatchset || undefined,
              paths: [path],
              toPatchset: selectedToPatchset || undefined
            });
            return {
              file: diffFileForResponse(
                path,
                fileMetadataByPath.get(path)?.changeKind,
                response.diff
              )
            };
          },
          retry: false,
          staleTime: Infinity
        }))
      : []
  });

  const fileStates = useMemo<DiffViewerFileState[] | undefined>(() => {
    if (!usesLazyFileDiffs) {
      return undefined;
    }

    return changedPaths.map((path, index) => {
      const query = fileDiffQueries[index];
      const metadata = fileMetadataByPath.get(path) ?? {};

      if (query?.data) {
        return {
          ...metadata,
          file: query.data.file,
          path,
          status: "loaded"
        };
      }
      if (query?.isError) {
        return {
          ...metadata,
          error: query.error,
          path,
          status: "error"
        };
      }
      if (query?.isFetching) {
        return { ...metadata, path, status: "loading" };
      }
      return { ...metadata, path, status: "pending" };
    });
  }, [changedPaths, fileMetadataByPath, fileDiffQueries, usesLazyFileDiffs]);

  const retryFileDiff = useCallback(
    (path: string) => {
      requestFileDiff(path);
      const index = changedPaths.indexOf(path);
      if (index >= 0) {
        void fileDiffQueries[index]?.refetch();
      }
    },
    [changedPaths, fileDiffQueries, requestFileDiff]
  );

  return {
    patchsets,
    fromPatchset,
    selectedToPatchset,
    selectedFromPatchset,
    selectedToPatchsetMetadata,
    changedPaths,
    fileMetadataByPath,
    usesLazyFileDiffs,
    comparisonKey,
    diffQuery,
    fileStates,
    requestFileDiff,
    retryFileDiff
  };
}
