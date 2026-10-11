import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import type { FileChangeKind } from "../../components/diff/parseDiff";
import { BinaryViewer } from "../../components/source/BinaryViewer";
import { ImageViewer, imageMimeTypeFromPath } from "../../components/source/ImageViewer";
import { SourceCodeViewer } from "../../components/source/SourceCodeViewer";
import { binaryKindFromPath, decodeBase64File } from "../../components/source/sourceUtils";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { cn } from "../../lib/cn";

export type FileVersionSide = "original" | "changed";

export interface FileVersionTarget {
  /** The file's path in the diff, with or without a leading slash. */
  path: string;
  /** The path before a rename. */
  oldPath?: string;
  changeKind?: FileChangeKind;
}

interface FileVersionDrawerProps {
  api: ApiClient;
  /** Labels for the two sides, such as "Original" and "Changed". */
  changedLabel: string;
  /** The tree holding the changed file: the patchset's result. */
  changedTreeId?: string;
  onClose(): void;
  onSideChange(side: FileVersionSide): void;
  originalLabel: string;
  /** The tree holding the original file: what the patchset is based on. */
  originalTreeId?: string;
  side: FileVersionSide;
  slice?: SliceRef;
  target?: FileVersionTarget;
}

function repositoryPath(path: string) {
  return `/${path.replace(/^\/+/, "")}`;
}

/** Which sides a file has: an added file has no original, a deleted one no changed version. */
export function fileVersionSides(changeKind: FileChangeKind | undefined) {
  return {
    original: changeKind !== "added",
    changed: changeKind !== "deleted"
  };
}

/**
 * The whole file a changeset's diff shows part of, as it was before the
 * change (the patchset's base tree) or after it (the patchset's result tree),
 * so a reviewer can read it in full without leaving the changeset.
 */
export function FileVersionDrawer({
  api,
  changedLabel,
  changedTreeId,
  onClose,
  onSideChange,
  originalLabel,
  originalTreeId,
  side,
  slice,
  target
}: FileVersionDrawerProps) {
  const open = Boolean(target);
  const sides = fileVersionSides(target?.changeKind);
  // A side the file does not have falls back to the one it has.
  const shownSide: FileVersionSide = sides[side] ? side : side === "original" ? "changed" : "original";
  const treeId = shownSide === "original" ? originalTreeId : changedTreeId;
  const path = target
    ? repositoryPath(shownSide === "original" && target.oldPath ? target.oldPath : target.path)
    : "";
  const fileQuery = useQuery({
    enabled: Boolean(open && treeId && path),
    queryKey: ["changesetFileVersion", treeId, path, slice?.account, slice?.slice],
    queryFn: () => api.readFile({ path, rootTreeId: treeId, slice }),
    staleTime: Infinity
  });
  const isImage = Boolean(imageMimeTypeFromPath(path));
  const binaryKind = binaryKindFromPath(path);
  const data = fileQuery.data?.data ?? "";

  return (
    <>
      {open ? (
        <button
          aria-label="Close file"
          className="fixed inset-0 z-40 bg-black/30"
          onClick={onClose}
          type="button"
        />
      ) : null}
      <aside
        aria-hidden={!open}
        aria-label="File"
        className={cn(
          "fixed inset-y-0 right-0 z-50 flex w-full max-w-5xl transform flex-col bg-white shadow-xl transition-transform duration-200 dark:bg-zinc-900",
          open ? "translate-x-0" : "translate-x-full"
        )}
      >
        <div className="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                {path}
              </h2>
              {target && slice?.account && slice.slice && sides.changed ? (
                <Link
                  className="mt-1 inline-block text-xs text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                  params={{ account: slice.account, slice: slice.slice } as never}
                  search={{ path: repositoryPath(target.path) } as never}
                  to="/slices/$account/$slice"
                >
                  Open the latest version in {slice.account}/{slice.slice}
                </Link>
              ) : null}
            </div>
            <button
              className="shrink-0 rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98] dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
              onClick={onClose}
              type="button"
            >
              Close
            </button>
          </div>
          <div className="mt-3 inline-flex rounded-md border border-slate-300 p-0.5 text-xs font-semibold dark:border-zinc-700" role="group">
            {(["original", "changed"] as const).map((value) => (
              <button
                aria-pressed={shownSide === value}
                className={cn(
                  "rounded px-2.5 py-1 transition disabled:cursor-not-allowed disabled:opacity-40",
                  shownSide === value
                    ? "bg-zinc-950 text-white dark:bg-zinc-100 dark:text-zinc-950"
                    : "text-slate-700 hover:bg-slate-100 dark:text-zinc-300 dark:hover:bg-zinc-800"
                )}
                disabled={!sides[value]}
                key={value}
                onClick={() => onSideChange(value)}
                title={
                  sides[value]
                    ? undefined
                    : value === "original"
                      ? "The file is new in this change."
                      : "This change deletes the file."
                }
                type="button"
              >
                {value === "original" ? originalLabel : changedLabel}
              </button>
            ))}
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
          {!open ? null : !treeId ? (
            <SliceNotice title="This version is not available.">
              The patchset does not record its {shownSide === "original" ? "base" : "result"} tree.
            </SliceNotice>
          ) : fileQuery.isPending ? (
            <div className="h-64 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
          ) : fileQuery.isError && /not found/i.test(String(fileQuery.error)) ? (
            <SliceNotice title={`No ${path} in this version.`}>
              {shownSide === "original"
                ? "The file is new in this change; see the changed version."
                : "This change removes or moves the file; see the original version."}
            </SliceNotice>
          ) : fileQuery.isError ? (
            <SliceNotice title="Could not load the file" tone="error">
              {fileQuery.error instanceof Error ? fileQuery.error.message : String(fileQuery.error)}
            </SliceNotice>
          ) : isImage ? (
            <ImageViewer data={data} path={path} />
          ) : binaryKind ? (
            <BinaryViewer data={data} kind={binaryKind} path={path} />
          ) : (
            <SourceCodeViewer code={decodeBase64File(data)} path={path} />
          )}
        </div>
      </aside>
    </>
  );
}
