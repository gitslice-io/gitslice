import { useEffect, useState } from "react";
import { type RevealState } from "./collapse";
import { type DiffFile } from "./parseDiff";

import {
  type DiffViewerFileState,
  type DiffFileLoadStatus
} from "./DiffViewer";
import {
  type ExpandFn,
  type ViewMode,
  largeDiffLineLimit,
  errorMessage,
  estimatedDiffBodyHeight
} from "./diffViewShared";
import { ChangeKindBadge } from "./ChangeKindBadge";
import { UnifiedDiff } from "./UnifiedDiff";
import { SplitDiff } from "./SplitDiff";

export function DiffFilePanel({
  file,
  fileState,
  isBodyMounted,
  onExpand,
  onRetry,
  onViewFile,
  refCallback,
  revealed,
  viewMode
}: {
  file: DiffFile;
  fileState?: DiffViewerFileState;
  isBodyMounted: boolean;
  onExpand: ExpandFn;
  onRetry?(path: string): void;
  onViewFile?(file: DiffFile): void;
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
        <div className="flex shrink-0 items-center gap-2">
          {status === "loaded" ? (
            <div className="flex gap-2 font-mono text-xs">
              <span className="rounded bg-emerald-50 dark:bg-emerald-950/30 px-1.5 py-0.5 text-emerald-800 dark:text-emerald-200 sm:px-2 sm:py-1">
                +{file.additions}
              </span>
              <span className="rounded bg-rose-50 dark:bg-rose-950/30 px-1.5 py-0.5 text-rose-800 dark:text-rose-200 sm:px-2 sm:py-1">
                -{file.deletions}
              </span>
            </div>
          ) : null}
          {onViewFile ? (
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98] sm:px-2 sm:py-1"
              onClick={() => onViewFile({ ...file, changeKind: fileState?.changeKind ?? file.changeKind, oldPath: file.oldPath ?? fileState?.oldPath })}
              title="Read the whole file, before or after this change"
              type="button"
            >
              View file
            </button>
          ) : null}
        </div>
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

export function DiffSkeleton() {
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

export function DiffErrorBox({ message }: { message: string }) {
  return (
    <div className="rounded-lg border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-4 py-3 text-sm text-rose-800 dark:text-rose-200">
      {message}
    </div>
  );
}
