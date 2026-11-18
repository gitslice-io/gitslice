import { useCallback, useEffect, useMemo, useState } from "react";

import type { DiffChangesetResponse } from "../../api/types";
import { cn } from "../../lib/cn";
import { ChangedFilesTree } from "./ChangedFilesTree";
import { DiffErrorBox, DiffFilePanel, DiffSkeleton } from "./DiffFilePanel";
import { errorMessage, type ViewMode } from "./diffViewShared";
import { FilePickerSheet, MobileFileSwitcher } from "./FileSwitcher";
import {
  diffFileId,
  parseDiff,
  type DiffFile,
  type FileChangeKind
} from "./parseDiff";
import { isGeneratedFile } from "./reviewState";
import { useDiffViewportController } from "./useDiffViewportController";

interface DiffViewerProps {
  diffResponse?: DiffChangesetResponse;
  error: unknown;
  isError: boolean;
  isLoading: boolean;
  focusFilePath?: string;
  fileStates?: DiffViewerFileState[];
  onFileNeeded?(path: string): void;
  onFileRetry?(path: string): void;
  onViewFile?(file: DiffFile): void;
  viewedScope?: string;
  viewMode?: ViewMode;
  onViewModeChange?(mode: ViewMode): void;
}

export type DiffFileLoadStatus =
  | "loaded"
  | "loading"
  | "error"
  | "pending";

export interface DiffViewerFileState {
  changeKind?: FileChangeKind;
  error?: unknown;
  file?: DiffFile;
  oldPath?: string;
  path: string;
  status: DiffFileLoadStatus;
}

export function DiffViewer({
  diffResponse,
  error,
  isError,
  isLoading,
  focusFilePath,
  fileStates,
  onFileNeeded,
  onFileRetry,
  onViewFile,
  viewedScope,
  viewMode: viewModeProp,
  onViewModeChange
}: DiffViewerProps) {
  const parsedFiles = useMemo(
    () =>
      parseDiff(diffResponse?.diff ?? "", diffResponse?.changedPaths ?? []),
    [diffResponse?.changedPaths, diffResponse?.diff]
  );
  const files = useMemo(
    () =>
      fileStates === undefined
        ? parsedFiles
        : fileStates.map(navigationFileForState),
    [fileStates, parsedFiles]
  );
  const fileStatesById = useMemo(
    () =>
      new Map(
        (fileStates ?? []).map((state) => [diffFileId(state.path), state])
      ),
    [fileStates]
  );

  const [ownViewMode, setOwnViewMode] = useState<ViewMode>("unified");
  const viewMode = viewModeProp ?? ownViewMode;
  const changeViewMode = onViewModeChange ?? setOwnViewMode;
  const [filePickerOpen, setFilePickerOpen] = useState(false);

  const {
    activeId,
    currentActiveId,
    mountedDiffBodies,
    viewedIds,
    revealed,
    expandGap,
    isCollapsed,
    toggleCollapsed,
    toggleViewed,
    selectFile,
    setPanelRef
  } = useDiffViewportController({
    files,
    diffText: diffResponse?.diff,
    focusFilePath,
    viewedScope,
    onFileNeeded
  });

  const changedPathCount =
    fileStates?.length ?? diffResponse?.changedPaths?.length ?? 0;
  const changedCount = changedPathCount > 0 ? changedPathCount : files.length;
  const totalAdditions = files.reduce((total, file) => total + file.additions, 0);
  const totalDeletions = files.reduce((total, file) => total + file.deletions, 0);
  const hasTextualDiff =
    fileStates !== undefined
      ? files.length > 0
      : (diffResponse?.diff ?? "").trim().length > 0 && files.length > 0;
  const canUseMobileFileSwitcher =
    !isLoading && hasTextualDiff && files.length > 0;

  const openFilePicker = useCallback(() => setFilePickerOpen(true), []);
  const closeFilePicker = useCallback(() => setFilePickerOpen(false), []);

  useEffect(() => {
    if (!canUseMobileFileSwitcher) {
      setFilePickerOpen(false);
    }
  }, [canUseMobileFileSwitcher]);

  return (
    <>
      <section className="mt-2.5 rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50 md:mt-3">
        <div className="sticky top-[var(--page-header-height,3rem)] z-20 border-b border-slate-200 dark:border-zinc-800 bg-white/95 dark:bg-zinc-900/95 px-3 py-2 backdrop-blur md:px-5 md:py-3 lg:static lg:z-auto lg:bg-white dark:lg:bg-zinc-900 lg:py-3 lg:backdrop-blur-none">
          <div className="flex items-center justify-between gap-2 md:gap-3">
            <div className="min-w-0 flex-1 lg:flex-none">
              <div className="hidden flex-wrap items-baseline gap-x-2 gap-y-0.5 lg:flex">
                <h2 className="truncate text-sm font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 md:text-base">
                  Files
                </h2>
                <span className="text-xs text-slate-500 dark:text-zinc-400 md:text-sm">
                  {isLoading ? "Loading…" : `${changedCount} changed`}
                </span>
                {!isLoading && files.length > 0 ? (
                  <span className="flex gap-1.5 font-mono text-xs">
                    <span className="text-emerald-700 dark:text-emerald-300">+{totalAdditions}</span>
                    <span className="text-rose-700 dark:text-rose-300">-{totalDeletions}</span>
                  </span>
                ) : null}
                {!isLoading && files.length > 1 ? (
                  <span
                    className="text-xs text-slate-500 dark:text-zinc-400"
                    title="j and k: next and previous file. v: mark the file viewed."
                  >
                    {viewedIds.size} of {files.length} viewed
                  </span>
                ) : null}
              </div>
              {canUseMobileFileSwitcher ? (
                <MobileFileSwitcher
                  activeId={currentActiveId}
                  files={files}
                  onOpenPicker={openFilePicker}
                  onSelectFile={selectFile}
                />
              ) : (
                <div className="flex min-h-8 flex-wrap items-center gap-x-2 gap-y-0.5 lg:hidden">
                  <h2 className="truncate text-sm font-semibold tracking-normal text-zinc-950 dark:text-zinc-50">
                    Files
                  </h2>
                  <span className="text-xs text-slate-500 dark:text-zinc-400">
                    {isLoading ? "Loading…" : `${changedCount} changed`}
                  </span>
                </div>
              )}
            </div>
            <div className="flex shrink-0 items-center justify-end gap-1.5 md:gap-2">
              {!isLoading && hasTextualDiff ? (
                <ViewModeToggle value={viewMode} onChange={changeViewMode} />
              ) : null}
            </div>
          </div>
        </div>

        {isLoading ? (
          <DiffSkeleton />
        ) : isError ? (
          <div className="p-5 md:p-6">
            <DiffErrorBox message={errorMessage(error)} />
          </div>
        ) : hasTextualDiff ? (
          <div className="grid gap-3 p-2 sm:p-3 md:p-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
            <aside className="hidden lg:sticky lg:top-20 lg:block lg:self-start">
              <ChangedFilesTree
                activeId={activeId}
                files={files}
                onSelect={selectFile}
                viewedIds={viewedIds}
              />
            </aside>
            <div className="grid grid-cols-1 min-w-0 gap-3 md:gap-4">
              {files.map((file) => (
                <DiffFilePanel
                  collapsed={isCollapsed(file)}
                  file={file}
                  fileState={fileStatesById.get(file.id)}
                  generated={isGeneratedFile(file)}
                  isBodyMounted={mountedDiffBodies.has(file.id)}
                  key={file.id}
                  onExpand={expandGap}
                  onRetry={onFileRetry}
                  onToggleCollapsed={() => toggleCollapsed(file)}
                  onToggleViewed={() => toggleViewed(file)}
                  onViewFile={onViewFile}
                  refCallback={(node) => {
                    setPanelRef(file.id, node);
                  }}
                  revealed={revealed}
                  viewed={viewedIds.has(file.id)}
                  viewMode={viewMode}
                />
              ))}
            </div>
          </div>
        ) : (
          <div className="p-5 md:p-6">
            <div className="rounded-lg border border-dashed border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-950 px-4 py-6 text-sm text-slate-600 dark:text-zinc-400">
              No textual changes to display.
            </div>
          </div>
        )}
      </section>

      {filePickerOpen && canUseMobileFileSwitcher ? (
        <FilePickerSheet
          activeId={currentActiveId}
          files={files}
          onClose={closeFilePicker}
          onSelectFile={selectFile}
          totalAdditions={totalAdditions}
          totalDeletions={totalDeletions}
        />
      ) : null}
    </>
  );
}

function ViewModeToggle({
  onChange,
  value
}: {
  onChange(value: ViewMode): void;
  value: ViewMode;
}) {
  return (
    <div className="inline-flex h-8 w-fit overflow-hidden rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 p-0.5 md:h-9">
      {(["unified", "split"] as const).map((mode) => (
        <button
          aria-pressed={value === mode}
          className={cn(
            "rounded px-2.5 text-xs font-medium capitalize transition md:px-3 md:text-sm",
            value === mode
              ? "bg-white dark:bg-zinc-900 text-zinc-950 dark:text-zinc-50 shadow-sm"
              : "text-slate-600 dark:text-zinc-400 hover:text-zinc-950 dark:hover:text-zinc-50"
          )}
          key={mode}
          onClick={() => onChange(mode)}
          type="button"
        >
          {mode}
        </button>
      ))}
    </div>
  );
}

function navigationFileForState(state: DiffViewerFileState): DiffFile {
  return {
    additions: state.file?.additions ?? 0,
    changeKind: state.file?.changeKind ?? state.changeKind ?? "pending",
    deletions: state.file?.deletions ?? 0,
    id: diffFileId(state.path),
    lines: state.file?.lines ?? [],
    oldPath: state.file?.oldPath ?? state.oldPath,
    path: state.path,
    rows: state.file?.rows ?? []
  };
}
