import { useMemo, useEffect, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";

import type { ApiClient } from "../../api/useApi";
import { SlicePanel } from "../../components/slices/SlicePageParts";
import {
  listDirectoryAll,
  isSliceProjectionDirectoryPath
} from "../../components/source/sourceUtils";
import {
  buildSourceTree,
  flattenTreeRows,
  initialTreeExpansion,
  ancestorDirectoryPaths,
  parentAncestorDirectoryPaths,
  normalizeTreePath,
  compareRepositoryPaths,
  type DirectoryLoadResult,
  type SourceTreeRow
} from "./sourceTree";
import { TreeRow } from "./TreeRow";
import { SourceSkeleton, NavigatorSkeleton } from "./skeletons";

interface SliceFolderNavigatorProps {
  api: ApiClient;
  commitId: string;
  includedPaths: string[];
  isLatestLoading: boolean;
  isSelectedDirectory: boolean;
  onCollapse?(): void;
  onSelectPath(path: string): void;
  selectedPath: string;
  sliceId: string;
  sliceRef: import("../../api/types").SliceRef | undefined;
}

export function SliceFolderNavigator({
  api,
  commitId,
  includedPaths,
  isLatestLoading,
  isSelectedDirectory,
  onCollapse,
  onSelectPath,
  selectedPath,
  sliceId,
  sliceRef
}: SliceFolderNavigatorProps) {
  const initialExpandedPaths = useMemo(
    () => initialTreeExpansion(selectedPath, includedPaths, isSelectedDirectory),
    [includedPaths, isSelectedDirectory, selectedPath]
  );
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(
    () => initialExpandedPaths
  );

  useEffect(() => {
    setExpandedPaths((current) => {
      const next = new Set(current);
      const selectedExpansion = isSelectedDirectory
        ? ancestorDirectoryPaths(selectedPath)
        : parentAncestorDirectoryPaths(selectedPath);
      for (const path of selectedExpansion) {
        next.add(path);
      }
      return next.size === current.size ? current : next;
    });
  }, [isSelectedDirectory, selectedPath]);

  useEffect(() => {
    setExpandedPaths((current) => {
      const next = new Set(current);
      for (const path of includedPaths.flatMap((includedPath) =>
        ancestorDirectoryPaths(normalizeTreePath(includedPath))
      )) {
        next.add(path);
      }
      return next.size === current.size ? current : next;
    });
  }, [includedPaths]);

  const expandedDirectoryPaths = useMemo(
    () => Array.from(expandedPaths).sort(compareRepositoryPaths),
    [expandedPaths]
  );

  const directoryQueries = useQueries({
    queries: expandedDirectoryPaths.map((path) => ({
      enabled: Boolean(
        commitId && sliceRef?.account && sliceRef?.slice && !isLatestLoading
      ),
      queryKey: [
        "sliceTreeDirectory",
        sliceId,
        commitId,
        path,
        sliceRef?.account,
        sliceRef?.slice
      ],
      queryFn: () =>
        listDirectoryAll(api, {
          allowMissingDirectory: isSliceProjectionDirectoryPath(
            path,
            includedPaths
          ),
          commitId,
          path,
          slice: sliceRef
        })
    }))
  });

  const directoryResults = useMemo(
    () =>
      expandedDirectoryPaths.map((path, index) => ({
        data: directoryQueries[index]?.data ?? [],
        error: directoryQueries[index]?.error ?? null,
        isLoading: Boolean(directoryQueries[index]?.isPending),
        path
      })),
    [directoryQueries, expandedDirectoryPaths]
  );

  const root = useMemo(
    () => buildSourceTree(includedPaths, directoryResults),
    [directoryResults, includedPaths]
  );
  const rows = useMemo(
    () => flattenTreeRows(root, expandedPaths),
    [expandedPaths, root]
  );
  const isRootLoading = Boolean(
    directoryResults.find((result) => result.path === "")?.isLoading
  );

  function togglePath(path: string) {
    setExpandedPaths((current) => {
      const next = new Set(current);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  }

  return (
    <SlicePanel className="max-h-[60dvh] overflow-auto p-0 lg:min-h-full lg:max-h-none lg:overflow-visible">
      <div className="border-b border-slate-200 dark:border-zinc-800 px-3 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">Files</h2>
            <p className="mt-1 truncate text-xs leading-5 text-slate-500 dark:text-zinc-400">
              Slice-projected source tree
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
              onClick={() => onSelectPath("")}
              type="button"
            >
              Root
            </button>
            {onCollapse ? (
              <button
                aria-controls="slice-file-tree-panel"
                aria-expanded={true}
                aria-label="Hide files"
                className="hidden h-7 w-7 items-center justify-center rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-sm leading-none text-slate-600 dark:text-zinc-400 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98] lg:inline-flex"
                onClick={onCollapse}
                title="Hide files"
                type="button"
              >
                «
              </button>
            ) : null}
          </div>
        </div>
      </div>

      <div className="py-2">
        {isLatestLoading || (isRootLoading && !rows.length) ? (
          <div className="px-3 py-2">
            <NavigatorSkeleton />
          </div>
        ) : rows.length ? (
          rows.map((row) => (
            <TreeRow
              depth={row.depth}
              isExpanded={expandedPaths.has(row.node.path)}
              isLoading={Boolean(
                directoryResults.find((result) => result.path === row.node.path)
                  ?.isLoading
              )}
              key={row.node.path || "root-row"}
              node={row.node}
              onSelect={onSelectPath}
              onToggle={togglePath}
              selectedPath={selectedPath}
            />
          ))
        ) : (
          <p className="mx-3 rounded-md border border-dashed border-slate-300 dark:border-zinc-700 p-3 text-sm text-slate-600 dark:text-zinc-400">
            No files in this slice.
          </p>
        )}

        {directoryResults
          .filter((result) => result.error)
          .map((result) => (
            <p
              className="mx-3 mt-2 rounded-md bg-rose-50 dark:bg-rose-950/30 p-3 text-sm text-rose-800 dark:text-rose-200"
              key={`error-${result.path || "root"}`}
            >
              Could not load {result.path || "slice root"}:{" "}
              {result.error?.message}
            </p>
          ))}
      </div>
    </SlicePanel>
  );
}