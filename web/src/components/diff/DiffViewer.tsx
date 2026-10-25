import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { DiffChangesetResponse } from "../../api/types";
import { cn } from "../../lib/cn";
import { ChangedFilesTree } from "./ChangedFilesTree";
import { type RevealState } from "./collapse";
import { fileFingerprint, isGeneratedFile, isViewed } from "./reviewState";
import { useViewedFiles } from "./useViewedFiles";
import { FilePickerSheet, MobileFileSwitcher } from "./FileSwitcher";
import {
  diffFileId,
  parseDiff,
  type DiffFile,
  type FileChangeKind
} from "./parseDiff";

import {
  type ViewMode,
  type ExpandFn,
  EMPTY_REVEAL,
  EXPAND_CHUNK,
  errorMessage
} from "./diffViewShared";
import { DiffSkeleton, DiffErrorBox, DiffFilePanel } from "./DiffFilePanel";

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
  /** Opens the whole file, before or after the change. */
  onViewFile?(file: DiffFile): void;
  /**
   * What the reviewer's "Viewed" marks belong to (the changeset). They are kept
   * on this browser; without it they last until the page closes.
   */
  viewedScope?: string;
  /**
   * Unified or split. The changeset page keeps it in the URL (`?diff=split`) so
   * a link opens in the mode it was shared in; without it the viewer keeps its
   * own, starting unified.
   */
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

const lazyDiffBodyRootMargin = "1200px 0px 1200px 0px";

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
  const [ownViewMode, setOwnViewMode] = useState<ViewMode>("unified");
  const viewMode = viewModeProp ?? ownViewMode;
  const changeViewMode = onViewModeChange ?? setOwnViewMode;
  const [activeId, setActiveId] = useState<string | undefined>();
  const [mountedDiffBodies, setMountedDiffBodies] = useState<Set<string>>(
    () => new Set()
  );
  const [filePickerOpen, setFilePickerOpen] = useState(false);
  const { viewed, mark } = useViewedFiles(viewedScope);
  // Files the reviewer opened or folded by hand. Without a say here, a file
  // is folded when it is viewed or generated.
  const [collapseOverride, setCollapseOverride] = useState<Record<string, boolean>>({});
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
  const [focusTargetId, setFocusTargetId] = useState<string | null>(null);
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

  // Reset collapse state whenever a new diff loads.
  useEffect(() => {
    setRevealed({});
    setCollapseOverride({});
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
    // The file list grows as lazily loaded diffs arrive; a file picked (or
    // deep-linked) stays picked while it is still in the list.
    if (pinnedActiveIdRef.current && !fileIds.has(pinnedActiveIdRef.current)) {
      pinnedActiveIdRef.current = null;
    }

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
    (id: string, behavior: ScrollBehavior = "smooth") => {
      pinnedActiveIdRef.current = id;
      setActiveId(id);
      mountFileBody(id);
      const path = filePathsById.get(id);
      if (path) {
        onFileNeeded?.(path);
      }
      window.setTimeout(() => {
        document.getElementById(id)?.scrollIntoView({
          behavior,
          block: "start"
        });
      }, 0);
    },
    [filePathsById, mountFileBody, onFileNeeded]
  );

  // Diffs keep loading around a deep-linked file and change the page's height;
  // keep the file in view until the user scrolls (which releases the pin), for
  // up to 20 seconds.
  useEffect(() => {
    if (!focusTargetId || typeof ResizeObserver === "undefined") {
      return;
    }
    const keepInView = () => {
      if (pinnedActiveIdRef.current === focusTargetId) {
        document.getElementById(focusTargetId)?.scrollIntoView({ block: "start" });
      }
    };
    const observer = new ResizeObserver(keepInView);
    observer.observe(document.body);
    const stop = window.setTimeout(() => observer.disconnect(), 20_000);
    return () => {
      observer.disconnect();
      window.clearTimeout(stop);
    };
  }, [focusTargetId]);

  // Honor a `?file=` deep link: once the diff contains the requested path, pin
  // and scroll to it. Runs after the files-changed effect above (which resets
  // the active file to the first panel), so the deep-linked file wins.
  useEffect(() => {
    if (!focusFilePath || !files.length) {
      return;
    }
    // Paths may come with or without the leading slash.
    const wanted = focusFilePath.replace(/^\/+/, "");
    const target = files.find((file) => file.path.replace(/^\/+/, "") === wanted);
    if (!target) {
      return;
    }
    if (focusAppliedRef.current === focusFilePath) {
      return;
    }
    focusAppliedRef.current = focusFilePath;
    // Jump at once: a smooth scroll passes the files above, which then load
    // their diffs and move the page under it.
    selectFile(target.id, "auto");
    setFocusTargetId(target.id);
  }, [files, focusFilePath, selectFile]);

  const viewedIds = useMemo(
    () => new Set(files.filter((file) => isViewed(file, viewed)).map((file) => file.id)),
    [files, viewed]
  );
  const isCollapsed = (file: DiffFile) =>
    collapseOverride[file.id] ?? (viewedIds.has(file.id) || isGeneratedFile(file));

  const toggleCollapsed = (file: DiffFile) => {
    const collapse = !isCollapsed(file);
    setCollapseOverride((current) => ({ ...current, [file.id]: collapse }));
    if (!collapse) {
      // Opened by hand: draw its body now, and fetch it if it has not come.
      mountFileBody(file.id);
      onFileNeeded?.(file.path);
    }
  };

  // Marking a file viewed folds it, as unmarking opens it; the file's diff
  // must have loaded, since the mark is of what it shows.
  const toggleViewed = (file: DiffFile) => {
    if (file.lines.length === 0) {
      return;
    }
    const wasViewed = viewedIds.has(file.id);
    mark(file.path, wasViewed ? null : fileFingerprint(file));
    setCollapseOverride((current) => ({ ...current, [file.id]: !wasViewed }));
    if (wasViewed) {
      mountFileBody(file.id);
    }
  };

  // j and k move between files, v marks the one in view as viewed.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) {
        return;
      }
      const target = event.target as HTMLElement | null;
      if (target && (target.isContentEditable || /^(?:input|textarea|select)$/i.test(target.tagName))) {
        return;
      }
      if (event.key !== "j" && event.key !== "k" && event.key !== "v") {
        return;
      }
      const index = Math.max(0, files.findIndex((file) => file.id === currentActiveId));
      if (event.key === "v") {
        if (files[index]) {
          toggleViewed(files[index]);
        }
        return;
      }
      const next = files[index + (event.key === "j" ? 1 : -1)];
      if (next) {
        selectFile(next.id);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
    // toggleViewed and selectFile read what is listed here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [files, currentActiveId, viewedIds, collapseOverride, selectFile]);

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
                    panelRefs.current[file.id] = node;
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
