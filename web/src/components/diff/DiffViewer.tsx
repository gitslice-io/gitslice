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
import {
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
}

type ViewMode = "unified" | "split";

interface MountedDiffBodies {
  files: DiffFile[];
  ids: Set<string>;
}

const diffViewStorageKey = "gitslice.diffView";
const lazyDiffBodyRootMargin = "1200px 0px 1200px 0px";
const minDiffBodyPlaceholderHeight = 48;

export function DiffViewer({
  diffResponse,
  error,
  isError,
  isLoading
}: DiffViewerProps) {
  const files = useMemo(
    () =>
      parseDiff(diffResponse?.diff ?? "", diffResponse?.changedPaths ?? []),
    [diffResponse?.changedPaths, diffResponse?.diff]
  );
  const [viewMode, setViewMode] = useState<ViewMode>(readStoredViewMode);
  const [activeId, setActiveId] = useState<string | undefined>();
  const [showDiffs, setShowDiffs] = useState(false);
  const [mountedDiffBodies, setMountedDiffBodies] = useState<MountedDiffBodies>(
    () => ({
      files: [],
      ids: new Set()
    })
  );
  const mountedFileIds =
    mountedDiffBodies.files === files ? mountedDiffBodies.ids : undefined;
  const [revealed, setRevealed] = useState<Record<string, RevealState>>({});
  const panelRefs = useRef<Record<string, HTMLElement | null>>({});
  const changedPathCount = diffResponse?.changedPaths?.length ?? 0;
  const changedCount = changedPathCount > 0 ? changedPathCount : files.length;
  const totalAdditions = files.reduce((total, file) => total + file.additions, 0);
  const totalDeletions = files.reduce((total, file) => total + file.deletions, 0);
  const hasTextualDiff =
    (diffResponse?.diff ?? "").trim().length > 0 && files.length > 0;

  useEffect(() => {
    if (typeof window !== "undefined") {
      window.localStorage.setItem(diffViewStorageKey, viewMode);
    }
  }, [viewMode]);

  useEffect(() => {
    if (
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia("(min-width: 1024px)").matches
    ) {
      setShowDiffs(true);
    }
  }, []);

  // Reset collapse state whenever a new diff loads.
  useEffect(() => {
    setRevealed({});
  }, [files]);

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

  useEffect(() => {
    if (!files.length) {
      setActiveId(undefined);
      return;
    }

    setActiveId((current) =>
      current && files.some((file) => file.id === current) ? current : files[0].id
    );
  }, [files]);

  useEffect(() => {
    const fileIds = new Set(files.map((file) => file.id));

    setMountedDiffBodies((current) => {
      const currentIds =
        current.files === files ? current.ids : new Set<string>();
      const next = new Set<string>();

      currentIds.forEach((id) => {
        if (fileIds.has(id)) {
          next.add(id);
        }
      });

      if (current.files === files && next.size === currentIds.size) {
        return current;
      }

      return { files, ids: next };
    });
  }, [files, showDiffs]);

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

    files.forEach((file) => {
      const panel = panelRefs.current[file.id];
      if (panel) {
        observer.observe(panel);
      }
    });

    return () => observer.disconnect();
  }, [files, showDiffs]);

  useEffect(() => {
    if (!files.length) {
      return;
    }

    if (typeof IntersectionObserver === "undefined") {
      setMountedDiffBodies({
        files,
        ids: new Set(files.map((file) => file.id))
      });
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

        setMountedDiffBodies((current) => {
          const currentIds =
            current.files === files ? current.ids : new Set<string>();
          const next = new Set(currentIds);
          let changed = false;

          visibleIds.forEach((id) => {
            if (!next.has(id)) {
              next.add(id);
              changed = true;
            }
          });

          if (current.files === files && !changed) {
            return current;
          }

          return { files, ids: next };
        });
      },
      {
        root: null,
        rootMargin: lazyDiffBodyRootMargin,
        threshold: 0
      }
    );

    files.forEach((file) => {
      const panel = panelRefs.current[file.id];
      if (panel) {
        observer.observe(panel);
      }
    });

    return () => observer.disconnect();
  }, [files]);

  const mountFileBody = (id: string) => {
    setMountedDiffBodies((current) => {
      const currentIds =
        current.files === files ? current.ids : new Set<string>();

      if (current.files === files && currentIds.has(id)) {
        return current;
      }

      const next = new Set(currentIds);
      next.add(id);
      return { files, ids: next };
    });
  };

  const selectFile = (id: string) => {
    setActiveId(id);
    mountFileBody(id);
    setShowDiffs(true);
    window.setTimeout(() => {
      document.getElementById(id)?.scrollIntoView({
        behavior: "smooth",
        block: "start"
      });
    }, 0);
  };

  return (
    <section className="mt-3 rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-3 py-3 md:px-5">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 className="truncate text-sm font-semibold tracking-normal text-zinc-950 md:text-base">
              Files
            </h2>
            <p className="mt-0.5 text-xs text-slate-600 md:text-sm">
              {isLoading
                ? "Loading file changes..."
                : `${changedCount} file(s) changed`}
            </p>
            {!isLoading && files.length > 0 ? (
              <div className="mt-1 flex gap-2 font-mono text-xs">
                <span className="rounded bg-emerald-50 px-2 py-1 text-emerald-800">
                  +{totalAdditions}
                </span>
                <span className="rounded bg-rose-50 px-2 py-1 text-rose-800">
                  -{totalDeletions}
                </span>
              </div>
            ) : null}
          </div>
          <div className="flex shrink-0 flex-wrap justify-end gap-2">
            <button
              aria-pressed={showDiffs}
              className={cn(
                "h-9 rounded-md border px-3 text-sm font-medium transition active:scale-[0.98]",
                showDiffs
                  ? "border-zinc-950 bg-zinc-950 text-white"
                  : "border-slate-200 bg-white text-slate-700 hover:border-slate-300"
              )}
              disabled={!hasTextualDiff}
              onClick={() => setShowDiffs((value) => !value)}
              type="button"
            >
              {showDiffs ? "Hide diff" : "Show diff"}
            </button>
            {showDiffs ? (
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
        <div
          className={cn(
            "grid gap-3 p-3 md:p-4",
            showDiffs && "lg:grid-cols-[20rem_minmax(0,1fr)]"
          )}
        >
          <aside
            className={cn(
              "lg:sticky lg:top-20 lg:self-start",
              !showDiffs && "lg:max-w-xl"
            )}
          >
            <ChangedFilesTree
              activeId={activeId}
              files={files}
              onSelect={selectFile}
            />
          </aside>
          {showDiffs ? (
            <div className="grid min-w-0 gap-3 md:gap-4">
              {files.map((file) => (
                <DiffFilePanel
                  file={file}
                  isBodyMounted={mountedFileIds?.has(file.id) ?? false}
                  key={file.id}
                  onExpand={expandGap}
                  refCallback={(node) => {
                    panelRefs.current[file.id] = node;
                  }}
                  revealed={revealed}
                  viewMode={viewMode}
                />
              ))}
            </div>
          ) : null}
        </div>
      ) : (
        <div className="p-5 md:p-6">
          <div className="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-600">
            No textual changes to display.
          </div>
        </div>
      )}
    </section>
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
    <div className="inline-flex h-9 w-fit overflow-hidden rounded-md border border-slate-200 bg-slate-50 p-0.5">
      {(["unified", "split"] as const).map((mode) => (
        <button
          aria-pressed={value === mode}
          className={cn(
            "rounded px-3 text-sm font-medium capitalize transition",
            value === mode
              ? "bg-white text-zinc-950 shadow-sm"
              : "text-slate-600 hover:text-zinc-950"
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
  isBodyMounted,
  onExpand,
  refCallback,
  revealed,
  viewMode
}: {
  file: DiffFile;
  isBodyMounted: boolean;
  onExpand: ExpandFn;
  refCallback(node: HTMLElement | null): void;
  revealed: Record<string, RevealState>;
  viewMode: ViewMode;
}) {
  return (
    <article
      className="scroll-mt-28 overflow-hidden rounded-lg border border-slate-200 bg-white"
      id={file.id}
      ref={refCallback}
    >
      <div className="flex flex-col gap-2 border-b border-slate-200 bg-slate-50 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-center gap-2">
          <ChangeKindBadge kind={file.changeKind} />
          <h3 className="min-w-0 truncate font-mono text-sm font-semibold text-zinc-950">
            {file.oldPath ? `${file.oldPath} -> ${file.path}` : file.path}
          </h3>
        </div>
        <div className="flex shrink-0 gap-2 font-mono text-xs">
          <span className="rounded bg-emerald-50 px-2 py-1 text-emerald-800">
            +{file.additions}
          </span>
          <span className="rounded bg-rose-50 px-2 py-1 text-rose-800">
            -{file.deletions}
          </span>
        </div>
      </div>
      {isBodyMounted ? (
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
      className="bg-white"
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
    <pre className="overflow-x-auto bg-white text-xs leading-5 md:text-sm">
      <code className="block min-w-max py-2">
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
                      "grid min-h-5 grid-cols-[3.5rem_3.5rem_minmax(0,1fr)] whitespace-pre px-4 py-0.5",
                      diffLineClass(line.kind)
                    )}
                    key={`${index}-${line.text}`}
                  >
                    <span className="select-none pr-3 text-right text-slate-400">
                      {line.oldNumber ?? ""}
                    </span>
                    <span className="select-none pr-3 text-right text-slate-400">
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
      <div className="bg-white px-4 py-5 text-sm text-slate-500">
        No line changes in this file.
      </div>
    );
  }

  return (
    <div className="overflow-x-auto bg-white text-xs leading-5 md:text-sm">
      <div className="min-w-[48rem] py-2">
        {segments.map((segment) =>
          segment.type === "gap" ? (
            <ExpandSeparator
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
  if (row.kind === "hunk") {
    return (
      <div
        className={cn(
          "border-t border-slate-100 px-4 py-1 font-mono first:border-t-0",
          row.hunkText?.startsWith("@@")
            ? "bg-sky-50 text-sky-700"
            : "bg-slate-50 text-slate-500"
        )}
      >
        {row.hunkText}
      </div>
    );
  }

  return (
    <div
      className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] border-t border-slate-100 first:border-t-0"
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
    </div>
  );
}

// A full-width separator standing in for a collapsed run of unchanged lines, with
// controls to reveal a chunk from the top, the whole gap, or a chunk from the
// bottom. Rendered with `flex` so it works inside both the unified <pre><code>
// and the split <div> bodies.
function ExpandSeparator({
  gap,
  keyFor,
  onExpand
}: {
  gap: { hiddenTotal: number; remaining: number };
  keyFor(): string;
  onExpand: ExpandFn;
}) {
  return (
    <span className="flex w-full items-center justify-center gap-3 border-y border-slate-100 bg-sky-50/70 px-4 py-1 font-mono text-xs text-sky-700 first:border-t-0">
      <button
        aria-label={`Expand ${EXPAND_CHUNK} lines up`}
        className="rounded px-1.5 hover:bg-sky-100"
        onClick={() => onExpand(keyFor(), "up", gap.hiddenTotal)}
        type="button"
      >
        ↑
      </button>
      <button
        aria-label="Expand all hidden lines"
        className="rounded px-2 hover:bg-sky-100 hover:underline"
        onClick={() => onExpand(keyFor(), "all", gap.hiddenTotal)}
        type="button"
      >
        ⋯ {gap.remaining} hidden {gap.remaining === 1 ? "line" : "lines"}
      </button>
      <button
        aria-label={`Expand ${EXPAND_CHUNK} lines down`}
        className="rounded px-1.5 hover:bg-sky-100"
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
    return <div className="min-h-6 bg-slate-50/80" />;
  }

  return (
    <div
      className={cn(
        "grid min-h-6 grid-cols-[3.5rem_minmax(0,1fr)] overflow-x-auto whitespace-pre px-4 py-0.5 font-mono",
        tone === "add" && "bg-emerald-50 text-emerald-900",
        tone === "del" && "bg-rose-50 text-rose-900",
        tone === "context" && "text-slate-700"
      )}
    >
      <span className="select-none pr-3 text-right text-slate-400">
        {tone === "add" ? line.newNumber : line.oldNumber}
      </span>
      <span>{line.content || " "}</span>
    </div>
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
          className="overflow-hidden rounded-lg border border-slate-200"
          key={index}
        >
          <div className="border-b border-slate-200 bg-slate-50 px-4 py-3">
            <div className="h-4 w-64 animate-pulse rounded bg-slate-200" />
          </div>
          <div className="space-y-2 p-4">
            <div className="h-4 w-full animate-pulse rounded bg-slate-100" />
            <div className="h-4 w-11/12 animate-pulse rounded bg-slate-100" />
            <div className="h-4 w-4/5 animate-pulse rounded bg-slate-100" />
            <div className="h-4 w-2/3 animate-pulse rounded bg-slate-100" />
          </div>
        </div>
      ))}
    </div>
  );
}

function DiffErrorBox({ message }: { message: string }) {
  return (
    <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
      {message}
    </div>
  );
}

function diffLineClass(kind: DiffLineKind) {
  switch (kind) {
    case "add":
      return "bg-emerald-50 text-emerald-900";
    case "del":
      return "bg-rose-50 text-rose-900";
    case "hunk":
      return "bg-sky-50 text-sky-700";
    case "meta":
      return "bg-slate-50 text-slate-500";
    case "context":
      return "text-slate-700";
  }
}

function changeKindClass(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "bg-emerald-100 text-emerald-800";
    case "deleted":
      return "bg-rose-100 text-rose-800";
    case "renamed":
      return "bg-violet-100 text-violet-800";
    case "modified":
      return "bg-amber-100 text-amber-900";
  }
}

function splitRowKey(row: DiffRow, index: number) {
  return `${index}-${row.left?.oldNumber ?? ""}-${row.right?.newNumber ?? ""}-${
    row.left?.text ?? row.right?.text ?? ""
  }`;
}

function estimatedDiffBodyHeight(file: DiffFile, viewMode: ViewMode) {
  const estimatedHeight =
    viewMode === "unified" ? file.lines.length * 20 : file.rows.length * 24;

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
