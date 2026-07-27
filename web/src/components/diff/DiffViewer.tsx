import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState
} from "react";

import type { DiffChangesetResponse } from "../../api/types";
import { cn } from "../../lib/cn";
import { ChangedFilesTree } from "./ChangedFilesTree";
import { computeSegments, type RevealState } from "./collapse";
import { FilePickerSheet, MobileFileSwitcher } from "./FileSwitcher";
import {
  diffFileId,
  parseDiff,
  type DiffFile,
  type DiffLine,
  type DiffLineKind,
  type DiffRow,
  type FileChangeKind
} from "./parseDiff";

// Show this many context lines next to each change; collapse the rest into
// expandable gaps. Expanding up/down reveals a chunk at a time.
const CONTEXT_LINES = 3;
const EXPAND_CHUNK = 20;
const EMPTY_REVEAL: RevealState = { top: 0, bottom: 0 };

type ExpandAction = "up" | "down" | "all";
type ExpandFn = (key: string, action: ExpandAction, hiddenTotal: number) => void;

function gapKey(fileId: string, viewMode: ViewMode, gapIndex: number) {
  return `${fileId}:${viewMode}:${gapIndex}`;
}

function rangeIndexes(start: number, end: number): number[] {
  const out: number[] = [];
  for (let index = start; index <= end; index += 1) {
    out.push(index);
  }
  return out;
}

interface DiffViewerProps {
  diffResponse?: DiffChangesetResponse;
  error: unknown;
  isError: boolean;
  isLoading: boolean;
  /**
   * Slice-relative path of a file to scroll to and pin as active on load (e.g.
   * a deep link from an agent transcript's `?file=` param). Applied once per
   * value, as soon as the diff contains a matching file.
   */
  focusFilePath?: string;
  fileStates?: DiffViewerFileState[];
  onFileNeeded?(path: string): void;
  onFileRetry?(path: string): void;
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

type ViewMode = "unified" | "split";

const diffViewStorageKey = "gitslice.diffView";
const lazyDiffBodyRootMargin = "1200px 0px 1200px 0px";
const minDiffBodyPlaceholderHeight = 48;
const largeDiffLineLimit = 5000;

export function DiffViewer({
  diffResponse,
  error,
  isError,
  isLoading,
  focusFilePath,
  fileStates,
  onFileNeeded,
  onFileRetry
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
  const fileListKey = files.map((file) => file.id).join("\0");
  const fileIds = useMemo(
    () => new Set(files.map((file) => file.id)),
    // The ids, rather than loaded file bodies, define list identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [fileListKey]
  );
  const filePathsById = useMemo(
    () => new Map(files.map((file) => [file.id, file.path])),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [fileListKey]
  );
  const fileStatesById = useMemo(
    () =>
      new Map(
        (fileStates ?? []).map((state) => [diffFileId(state.path), state])
      ),
    [fileStates]
  );
  const [viewMode, setViewMode] = useState<ViewMode>(readStoredViewMode);
  const [activeId, setActiveId] = useState<string | undefined>();
  const [mountedDiffBodies, setMountedDiffBodies] = useState<Set<string>>(
    () => new Set()
  );
  const [filePickerOpen, setFilePickerOpen] = useState(false);
  const [revealed, setRevealed] = useState<Record<string, RevealState>>({});
  const panelRefs = useRef<Record<string, HTMLElement | null>>({});
  // When the user explicitly picks a file (prev/next or the picker), pin it as
  // active so the scroll-spy doesn't immediately reassign it — e.g. jumping to a
  // short trailing file would otherwise report the preceding panel as active.
  // The pin is released on the next real scroll gesture (below).
  const pinnedActiveIdRef = useRef<string | null>(null);
  // Tracks the focusFilePath we have already scrolled to, so a deep link is
  // honored once rather than yanking the view back on every diff re-render.
  const focusAppliedRef = useRef<string | null>(null);
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
  const activeFileIndex = files.findIndex((file) => file.id === activeId);
  const currentActiveId =
    files[activeFileIndex >= 0 ? activeFileIndex : 0]?.id;
  const openFilePicker = useCallback(() => setFilePickerOpen(true), []);
  const closeFilePicker = useCallback(() => setFilePickerOpen(false), []);

  useEffect(() => {
    if (typeof window !== "undefined") {
      window.localStorage.setItem(diffViewStorageKey, viewMode);
    }
  }, [viewMode]);

  // Reset collapse state whenever a new diff loads.
  useEffect(() => {
    setRevealed({});
  }, [diffResponse?.diff, fileListKey]);

  useEffect(() => {
    if (!canUseMobileFileSwitcher) {
      setFilePickerOpen(false);
    }
  }, [canUseMobileFileSwitcher]);

  const expandGap = useCallback<ExpandFn>((key, action, hiddenTotal) => {
    setRevealed((current) => {
      const reveal = current[key] ?? EMPTY_REVEAL;
      let next: RevealState;
      if (action === "all") {
        next = { top: hiddenTotal, bottom: 0 };
      } else if (action === "up") {
        next = {
          top: Math.min(hiddenTotal - reveal.bottom, reveal.top + EXPAND_CHUNK),
          bottom: reveal.bottom
        };
      } else {
        next = {
          top: reveal.top,
          bottom: Math.min(hiddenTotal - reveal.top, reveal.bottom + EXPAND_CHUNK)
        };
      }
      return { ...current, [key]: next };
    });
  }, []);

  const firstFileId = files[0]?.id;

  useEffect(() => {
    pinnedActiveIdRef.current = null;

    if (!fileIds.size) {
      setActiveId(undefined);
      return;
    }

    setActiveId((current) =>
      current && fileIds.has(current) ? current : firstFileId
    );
  }, [fileIds, firstFileId]);

  // Release the active-file pin as soon as the user scrolls themselves, so the
  // scroll-spy resumes tracking whatever they scroll to. Programmatic
  // scrollIntoView does not emit these input events, so the pin survives the
  // jump it was set for.
  useEffect(() => {
    const release = () => {
      pinnedActiveIdRef.current = null;
    };
    window.addEventListener("wheel", release, { passive: true });
    window.addEventListener("touchmove", release, { passive: true });
    window.addEventListener("keydown", release);
    return () => {
      window.removeEventListener("wheel", release);
      window.removeEventListener("touchmove", release);
      window.removeEventListener("keydown", release);
    };
  }, []);

  useEffect(() => {
    setMountedDiffBodies((current) => {
      const next = new Set<string>();

      current.forEach((id) => {
        if (fileIds.has(id)) {
          next.add(id);
        }
      });

      if (next.size === current.size) {
        return current;
      }

      return next;
    });
  }, [fileIds]);

  useEffect(() => {
    if (!files.length || typeof IntersectionObserver === "undefined") {
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        const visibleEntries = entries
          .filter((entry) => entry.isIntersecting)
          .sort(
            (left, right) =>
              left.boundingClientRect.top - right.boundingClientRect.top
          );

        // Honor an explicit selection until the user scrolls (see selectFile).
        if (pinnedActiveIdRef.current) {
          return;
        }

        const topEntry = visibleEntries[0];
        if (topEntry?.target.id) {
          setActiveId(topEntry.target.id);
        }
      },
      {
        root: null,
        rootMargin: "-112px 0px -65% 0px",
        threshold: [0, 0.05, 0.2]
      }
    );

    fileIds.forEach((id) => {
      const panel = panelRefs.current[id];
      if (panel) {
        observer.observe(panel);
      }
    });

    return () => observer.disconnect();
  }, [fileIds]);

  useEffect(() => {
    if (!files.length) {
      return;
    }

    if (typeof IntersectionObserver === "undefined") {
      setMountedDiffBodies(new Set(fileIds));
      filePathsById.forEach((path) => onFileNeeded?.(path));
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        const visibleIds = entries
          .filter((entry) => entry.isIntersecting && entry.target.id)
          .map((entry) => entry.target.id);

        if (!visibleIds.length) {
          return;
        }

        visibleIds.forEach((id) => {
          const path = filePathsById.get(id);
          if (path) {
            onFileNeeded?.(path);
          }
        });

        setMountedDiffBodies((current) => {
          const next = new Set(current);
          let changed = false;

          visibleIds.forEach((id) => {
            if (!next.has(id)) {
              next.add(id);
              changed = true;
            }
          });

          if (!changed) {
            return current;
          }

          return next;
        });
      },
      {
        root: null,
        rootMargin: lazyDiffBodyRootMargin,
        threshold: 0
      }
    );

    fileIds.forEach((id) => {
      const panel = panelRefs.current[id];
      if (panel) {
        observer.observe(panel);
      }
    });

    return () => observer.disconnect();
  }, [fileIds, filePathsById, onFileNeeded]);

  const mountFileBody = useCallback(
    (id: string) => {
      setMountedDiffBodies((current) => {
        if (current.has(id)) {
          return current;
        }

        const next = new Set(current);
        next.add(id);
        return next;
      });
    },
    []
  );

  const selectFile = useCallback(
    (id: string) => {
      pinnedActiveIdRef.current = id;
      setActiveId(id);
      mountFileBody(id);
      const path = filePathsById.get(id);
      if (path) {
        onFileNeeded?.(path);
      }
      window.setTimeout(() => {
        document.getElementById(id)?.scrollIntoView({
          behavior: "smooth",
          block: "start"
        });
      }, 0);
    },
    [filePathsById, mountFileBody, onFileNeeded]
  );

  // Honor a `?file=` deep link: once the diff contains the requested path, pin
  // and scroll to it. Runs after the files-changed effect above (which resets
  // the active file to the first panel), so the deep-linked file wins.
  useEffect(() => {
    if (!focusFilePath || !files.length) {
      return;
    }
    if (focusAppliedRef.current === focusFilePath) {
      return;
    }
    const target = files.find((file) => file.path === focusFilePath);
    if (!target) {
      return;
    }
    focusAppliedRef.current = focusFilePath;
    selectFile(target.id);
  }, [files, focusFilePath, selectFile]);

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
                <ViewModeToggle value={viewMode} onChange={setViewMode} />
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
              />
            </aside>
            <div className="grid grid-cols-1 min-w-0 gap-3 md:gap-4">
              {files.map((file) => (
                <DiffFilePanel
                  file={file}
                  fileState={fileStatesById.get(file.id)}
                  isBodyMounted={mountedDiffBodies.has(file.id)}
                  key={file.id}
                  onExpand={expandGap}
                  onRetry={onFileRetry}
                  refCallback={(node) => {
                    panelRefs.current[file.id] = node;
                  }}
                  revealed={revealed}
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

function DiffFilePanel({
  file,
  fileState,
  isBodyMounted,
  onExpand,
  onRetry,
  refCallback,
  revealed,
  viewMode
}: {
  file: DiffFile;
  fileState?: DiffViewerFileState;
  isBodyMounted: boolean;
  onExpand: ExpandFn;
  onRetry?(path: string): void;
  refCallback(node: HTMLElement | null): void;
  revealed: Record<string, RevealState>;
  viewMode: ViewMode;
}) {
  const [showLargeDiff, setShowLargeDiff] = useState(false);
  const status = fileState?.status ?? "loaded";
  const isLargeDiff = file.lines.length > largeDiffLineLimit;

  useEffect(() => {
    setShowLargeDiff(false);
  }, [file.lines]);

  return (
    <article
      className="scroll-mt-32 overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 lg:scroll-mt-28"
      id={file.id}
      ref={refCallback}
    >
      <div className="flex flex-col gap-1.5 border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 sm:flex-row sm:items-center sm:justify-between sm:gap-2 sm:px-4 sm:py-3">
        <div className="flex min-w-0 items-center gap-2">
          <ChangeKindBadge kind={file.changeKind} />
          <h3 className="min-w-0 truncate font-mono text-xs font-semibold text-zinc-950 dark:text-zinc-50 sm:text-sm">
            {file.oldPath ? `${file.oldPath} -> ${file.path}` : file.path}
          </h3>
        </div>
        {status === "loaded" ? (
          <div className="flex shrink-0 gap-2 font-mono text-xs">
            <span className="rounded bg-emerald-50 dark:bg-emerald-950/30 px-1.5 py-0.5 text-emerald-800 dark:text-emerald-200 sm:px-2 sm:py-1">
              +{file.additions}
            </span>
            <span className="rounded bg-rose-50 dark:bg-rose-950/30 px-1.5 py-0.5 text-rose-800 dark:text-rose-200 sm:px-2 sm:py-1">
              -{file.deletions}
            </span>
          </div>
        ) : null}
      </div>
      {status !== "loaded" ? (
        <PendingDiffBody
          error={fileState?.error}
          onRetry={onRetry ? () => onRetry(file.path) : undefined}
          status={status}
        />
      ) : isLargeDiff && !showLargeDiff ? (
        <LargeDiffGuard
          lineCount={file.lines.length}
          onShow={() => setShowLargeDiff(true)}
        />
      ) : isBodyMounted || showLargeDiff ? (
        <DiffFileBody
          file={file}
          onExpand={onExpand}
          revealed={revealed}
          viewMode={viewMode}
        />
      ) : (
        <DiffBodyPlaceholder file={file} viewMode={viewMode} />
      )}
    </article>
  );
}

function PendingDiffBody({
  error,
  onRetry,
  status
}: {
  error?: unknown;
  onRetry?(): void;
  status: Exclude<DiffFileLoadStatus, "loaded">;
}) {
  if (status === "error") {
    return (
      <div className="flex min-h-20 flex-wrap items-center justify-between gap-3 bg-rose-50/60 dark:bg-rose-950/20 px-4 py-4 text-sm text-rose-800 dark:text-rose-200">
        <span>{errorMessage(error)}</span>
        {onRetry ? (
          <button
            className="rounded-md border border-rose-300 dark:border-rose-800 bg-white dark:bg-zinc-900 px-3 py-1.5 text-xs font-semibold transition hover:bg-rose-50 dark:hover:bg-rose-950/30 active:scale-[0.98]"
            onClick={onRetry}
            type="button"
          >
            Retry
          </button>
        ) : null}
      </div>
    );
  }

  return (
    <div className="flex min-h-20 items-center bg-white dark:bg-zinc-900 px-4 py-4 text-sm text-slate-500 dark:text-zinc-400">
      {status === "loading"
        ? "Loading diff…"
        : "Diff loads as this file nears the viewport."}
    </div>
  );
}

function LargeDiffGuard({
  lineCount,
  onShow
}: {
  lineCount: number;
  onShow(): void;
}) {
  return (
    <div className="flex min-h-20 flex-wrap items-center justify-between gap-3 bg-slate-50 dark:bg-zinc-950 px-4 py-4 text-sm text-slate-600 dark:text-zinc-400">
      <span>Large diff ({lineCount} lines)</span>
      <button
        className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-1.5 text-xs font-semibold text-zinc-950 dark:text-zinc-50 transition hover:bg-slate-100 dark:hover:bg-zinc-800 active:scale-[0.98]"
        onClick={onShow}
        type="button"
      >
        Show diff
      </button>
    </div>
  );
}

function DiffFileBody({
  file,
  onExpand,
  revealed,
  viewMode
}: {
  file: DiffFile;
  onExpand: ExpandFn;
  revealed: Record<string, RevealState>;
  viewMode: ViewMode;
}) {
  return viewMode === "unified" ? (
    <UnifiedDiff file={file} onExpand={onExpand} revealed={revealed} />
  ) : (
    <SplitDiff file={file} onExpand={onExpand} revealed={revealed} />
  );
}

function DiffBodyPlaceholder({
  file,
  viewMode
}: {
  file: DiffFile;
  viewMode: ViewMode;
}) {
  return (
    <div
      aria-hidden="true"
      className="bg-white dark:bg-zinc-900"
      style={{ height: estimatedDiffBodyHeight(file, viewMode) }}
    />
  );
}

function UnifiedDiff({
  file,
  onExpand,
  revealed
}: {
  file: DiffFile;
  onExpand: ExpandFn;
  revealed: Record<string, RevealState>;
}) {
  const segments = useMemo(
    () =>
      computeSegments(
        file.lines.length,
        (index) => file.lines[index].kind === "context",
        (index) =>
          file.lines[index].kind === "add" || file.lines[index].kind === "del",
        CONTEXT_LINES,
        (gapIndex) =>
          revealed[gapKey(file.id, "unified", gapIndex)] ?? EMPTY_REVEAL
      ),
    [file.id, file.lines, revealed]
  );

  return (
    <pre className="overflow-x-auto bg-white dark:bg-zinc-900 text-xs leading-4 md:text-[13px]">
      <code className="block min-w-max py-1.5">
        {segments.map((segment) =>
          segment.type === "gap" ? (
            <ExpandSeparator
              gap={segment}
              key={`gap-${segment.index}`}
              keyFor={() => gapKey(file.id, "unified", segment.index)}
              onExpand={onExpand}
            />
          ) : (
            <Fragment key={`block-${segment.start}`}>
              {rangeIndexes(segment.start, segment.end).map((index) => {
                const line = file.lines[index];
                return (
                  <span
                    className={cn(
                      "grid min-h-4 grid-cols-[1.75rem_minmax(0,1fr)] whitespace-pre px-1.5 sm:grid-cols-[3.5rem_3.5rem_minmax(0,1fr)] sm:px-4",
                      diffLineClass(line.kind)
                    )}
                    key={`${index}-${line.text}`}
                  >
                    <span className="hidden select-none pr-1 text-right text-[10px] text-slate-400 dark:text-zinc-500 sm:block sm:pr-3 sm:text-xs">
                      {line.oldNumber ?? ""}
                    </span>
                    <span className="select-none pr-1 text-right text-[10px] text-slate-400 dark:text-zinc-500 sm:pr-3 sm:text-xs">
                      {line.newNumber ?? ""}
                    </span>
                    <span>{line.text || " "}</span>
                  </span>
                );
              })}
            </Fragment>
          )
        )}
      </code>
    </pre>
  );
}

function SplitDiff({
  file,
  onExpand,
  revealed
}: {
  file: DiffFile;
  onExpand: ExpandFn;
  revealed: Record<string, RevealState>;
}) {
  const rows = file.rows;
  const segments = useMemo(
    () =>
      computeSegments(
        rows.length,
        (index) => rows[index].kind === "context",
        (index) =>
          rows[index].kind === "add" ||
          rows[index].kind === "del" ||
          rows[index].kind === "replace",
        CONTEXT_LINES,
        (gapIndex) =>
          revealed[gapKey(file.id, "split", gapIndex)] ?? EMPTY_REVEAL
      ),
    [file.id, rows, revealed]
  );

  if (rows.length === 0) {
    return (
      <div className="bg-white dark:bg-zinc-900 px-4 py-5 text-sm text-slate-500 dark:text-zinc-400">
        No line changes in this file.
      </div>
    );
  }

  return (
    <div className="overflow-x-auto bg-white dark:bg-zinc-900 text-xs leading-4 md:text-[13px]">
      <div className="grid min-w-full grid-cols-[1.75rem_minmax(16rem,max-content)_1.75rem_minmax(16rem,max-content)_1fr] py-1.5 sm:grid-cols-[3.5rem_minmax(24rem,max-content)_3.5rem_minmax(24rem,max-content)_1fr]">
        {segments.map((segment) =>
          segment.type === "gap" ? (
            <ExpandSeparator
              className="col-span-5"
              gap={segment}
              key={`gap-${segment.index}`}
              keyFor={() => gapKey(file.id, "split", segment.index)}
              onExpand={onExpand}
            />
          ) : (
            <Fragment key={`block-${segment.start}`}>
              {rangeIndexes(segment.start, segment.end).map((index) => (
                <SplitRow index={index} key={index} row={rows[index]} />
              ))}
            </Fragment>
          )
        )}
      </div>
    </div>
  );
}

function SplitRow({ index, row }: { index: number; row: DiffRow }) {
  if (row.kind === "hunk" || row.kind === "meta") {
    return (
      <div
        className={cn(
          "col-span-5 border-t border-slate-100 dark:border-zinc-800 px-4 py-0.5 font-mono first:border-t-0",
          row.kind === "hunk" && row.hunkText?.startsWith("@@")
            ? "bg-sky-50 dark:bg-sky-950/30 text-sky-700 dark:text-sky-300"
            : "bg-slate-50 dark:bg-zinc-950 text-slate-500 dark:text-zinc-400"
        )}
      >
        {row.hunkText}
      </div>
    );
  }

  return (
    <div
      className="col-span-5 grid grid-cols-subgrid border-t border-slate-100 dark:border-zinc-800 first:border-t-0"
      key={splitRowKey(row, index)}
    >
      <SplitCell
        line={row.left}
        tone={row.kind === "del" || row.kind === "replace" ? "del" : "context"}
      />
      <SplitCell
        line={row.right}
        tone={row.kind === "add" || row.kind === "replace" ? "add" : "context"}
      />
      <div className="bg-white dark:bg-zinc-900" />
    </div>
  );
}

// A full-width separator standing in for a collapsed run of unchanged lines, with
// controls to reveal a chunk from the top, the whole gap, or a chunk from the
// bottom. Rendered with `flex` so it works inside both the unified <pre><code>
// and the split <div> bodies.
function ExpandSeparator({
  className,
  gap,
  keyFor,
  onExpand
}: {
  className?: string;
  gap: { hiddenTotal: number; remaining: number };
  keyFor(): string;
  onExpand: ExpandFn;
}) {
  return (
    <span
      className={cn(
        "flex w-full items-center justify-center gap-3 border-y border-slate-100 dark:border-zinc-800 bg-sky-50/70 px-4 py-1 font-mono text-xs text-sky-700 dark:text-sky-300 first:border-t-0",
        className
      )}
    >
      <button
        aria-label={`Expand ${EXPAND_CHUNK} lines up`}
        className="rounded px-1.5 hover:bg-sky-100 dark:hover:bg-sky-950/45"
        onClick={() => onExpand(keyFor(), "up", gap.hiddenTotal)}
        type="button"
      >
        ↑
      </button>
      <button
        aria-label="Expand all hidden lines"
        className="rounded px-2 hover:bg-sky-100 dark:hover:bg-sky-950/45 hover:underline"
        onClick={() => onExpand(keyFor(), "all", gap.hiddenTotal)}
        type="button"
      >
        ⋯ {gap.remaining} hidden {gap.remaining === 1 ? "line" : "lines"}
      </button>
      <button
        aria-label={`Expand ${EXPAND_CHUNK} lines down`}
        className="rounded px-1.5 hover:bg-sky-100 dark:hover:bg-sky-950/45"
        onClick={() => onExpand(keyFor(), "down", gap.hiddenTotal)}
        type="button"
      >
        ↓
      </button>
    </span>
  );
}

function SplitCell({
  line,
  tone
}: {
  line?: DiffLine;
  tone: "add" | "context" | "del";
}) {
  if (!line) {
    return <div className="col-span-2 min-h-4 bg-slate-50/80 dark:bg-zinc-950/80" />;
  }

  const toneClass = cn(
    tone === "add" && "bg-emerald-50 dark:bg-emerald-950/30 text-emerald-900 dark:text-emerald-200",
    tone === "del" && "bg-rose-50 dark:bg-rose-950/30 text-rose-900 dark:text-rose-200",
    tone === "context" && "text-slate-700 dark:text-zinc-300"
  );

  return (
    <>
      <span
        className={cn(
          "min-h-4 select-none whitespace-pre pl-2 pr-1 text-right text-[10px] text-slate-400 dark:text-zinc-500 sm:pl-4 sm:pr-3 sm:text-xs",
          toneClass
        )}
      >
        {tone === "add" ? line.newNumber : line.oldNumber}
      </span>
      <span className={cn("min-h-4 whitespace-pre pr-2 font-mono sm:pr-4", toneClass)}>
        {line.content || " "}
      </span>
    </>
  );
}

function ChangeKindBadge({ kind }: { kind: FileChangeKind }) {
  return (
    <span
      className={cn(
        "shrink-0 rounded px-2 py-1 text-xs font-semibold uppercase",
        changeKindClass(kind)
      )}
    >
      {kind}
    </span>
  );
}

function DiffSkeleton() {
  return (
    <div className="grid gap-4 p-4 md:p-5">
      {Array.from({ length: 2 }).map((_, index) => (
        <div
          className="overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800"
          key={index}
        >
          <div className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-4 py-3">
            <div className="h-4 w-64 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
          </div>
          <div className="space-y-2 p-4">
            <div className="h-4 w-full animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
            <div className="h-4 w-11/12 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
            <div className="h-4 w-4/5 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
            <div className="h-4 w-2/3 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
          </div>
        </div>
      ))}
    </div>
  );
}

function DiffErrorBox({ message }: { message: string }) {
  return (
    <div className="rounded-lg border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-4 py-3 text-sm text-rose-800 dark:text-rose-200">
      {message}
    </div>
  );
}

function diffLineClass(kind: DiffLineKind) {
  switch (kind) {
    case "add":
      return "bg-emerald-50 dark:bg-emerald-950/30 text-emerald-900 dark:text-emerald-200";
    case "del":
      return "bg-rose-50 dark:bg-rose-950/30 text-rose-900 dark:text-rose-200";
    case "hunk":
      return "bg-sky-50 dark:bg-sky-950/30 text-sky-700 dark:text-sky-300";
    case "meta":
      return "bg-slate-50 dark:bg-zinc-950 text-slate-500 dark:text-zinc-400";
    case "context":
      return "text-slate-700 dark:text-zinc-300";
  }
}

function changeKindClass(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "bg-emerald-100 dark:bg-emerald-950/45 text-emerald-800 dark:text-emerald-200";
    case "deleted":
      return "bg-rose-100 dark:bg-rose-950/45 text-rose-800 dark:text-rose-200";
    case "renamed":
      return "bg-violet-100 text-violet-800";
    case "modified":
      return "bg-amber-100 dark:bg-amber-950/45 text-amber-900 dark:text-amber-200";
    case "pending":
      return "bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400";
  }
}

function splitRowKey(row: DiffRow, index: number) {
  return `${index}-${row.left?.oldNumber ?? ""}-${row.right?.newNumber ?? ""}-${
    row.left?.text ?? row.right?.text ?? ""
  }`;
}

function estimatedDiffBodyHeight(file: DiffFile, viewMode: ViewMode) {
  const estimatedHeight =
    viewMode === "unified" ? file.lines.length * 16 : file.rows.length * 16;

  return Math.max(minDiffBodyPlaceholderHeight, estimatedHeight);
}

function readStoredViewMode(): ViewMode {
  if (typeof window === "undefined") {
    return "unified";
  }

  return window.localStorage.getItem(diffViewStorageKey) === "split"
    ? "split"
    : "unified";
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
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
