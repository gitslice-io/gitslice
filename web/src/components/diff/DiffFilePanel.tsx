import { useEffect, useState } from "react";

import { cn } from "../../lib/cn";
import { getErrorMessage } from "../../lib/errors";
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
  estimatedDiffBodyHeight,
  splitDisplayPath,
  stripPathPrefix
} from "./diffViewShared";
import { ChangeKindBadge } from "./ChangeKindBadge";
import { UnifiedDiff } from "./UnifiedDiff";
import { SplitDiff } from "./SplitDiff";

export function DiffFilePanel({
  collapsed = false,
  file,
  fileState,
  generated = false,
  isBodyMounted,
  onExpand,
  onRetry,
  onToggleCollapsed,
  onToggleViewed,
  onViewFile,
  pathPrefix,
  refCallback,
  revealed,
  viewed = false,
  viewMode
}: {
  // The body is hidden: the file was viewed, is generated, or was folded.
  collapsed?: boolean;
  file: DiffFile;
  fileState?: DiffViewerFileState;
  // A tool wrote this file; it starts collapsed.
  generated?: boolean;
  isBodyMounted: boolean;
  onExpand: ExpandFn;
  onRetry?(path: string): void;
  onToggleCollapsed?(): void;
  onToggleViewed?(): void;
  onViewFile?(file: DiffFile): void;
  pathPrefix?: string;
  refCallback(node: HTMLElement | null): void;
  revealed: Record<string, RevealState>;
  viewed?: boolean;
  viewMode: ViewMode;
}) {
  const [showLargeDiff, setShowLargeDiff] = useState(false);
  const status = fileState?.status ?? "loaded";
  const changedLineCount = file.additions + file.deletions;
  const isLargeDiff = changedLineCount > largeDiffLineLimit;
  const { dir, name } = splitDisplayPath(file.path, pathPrefix);
  const displayOldPath = file.oldPath ? stripPathPrefix(file.oldPath, pathPrefix) : undefined;
  const fullTitle = file.oldPath ? `${file.oldPath} -> ${file.path}` : file.path;

  useEffect(() => {
    setShowLargeDiff(false);
  }, [file.lines]);

  return (
    <article
      className="min-w-0 scroll-mt-28 overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 lg:scroll-mt-24"
      id={file.id}
      ref={refCallback}
    >
      <div className="flex flex-col gap-1 border-b border-slate-200/80 dark:border-zinc-800 bg-slate-50/80 dark:bg-zinc-950/80 px-2.5 py-2 sm:flex-row sm:items-center sm:justify-between sm:gap-2 sm:px-3.5 sm:py-2">
        <div className="flex min-w-0 items-center gap-1.5 sm:gap-2">
          {onToggleCollapsed ? (
            <button
              aria-controls={`${file.id}-body`}
              aria-expanded={!collapsed}
              aria-label={`${collapsed ? "Expand" : "Collapse"} ${file.path}`}
              className="-my-1 -ml-1 inline-flex size-7 shrink-0 items-center justify-center rounded-md text-slate-500 transition hover:bg-slate-200/70 hover:text-zinc-950 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
              onClick={onToggleCollapsed}
              title={collapsed ? "Show the diff" : "Hide the diff"}
              type="button"
            >
              <svg
                aria-hidden="true"
                className={cn("size-3.5 transition-transform", collapsed && "-rotate-90")}
                fill="none"
                stroke="currentColor"
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth="2"
                viewBox="0 0 16 16"
              >
                <path d="m4 6 4 4 4-4" />
              </svg>
            </button>
          ) : null}
          <ChangeKindBadge compact kind={file.changeKind} />
          <h3
            className="flex min-w-0 flex-1 items-baseline overflow-hidden font-mono text-xs sm:text-[13px]"
            title={fullTitle}
          >
            {displayOldPath ? (
              <span className="truncate font-semibold text-zinc-950 dark:text-zinc-50">
                {displayOldPath} {"->"} {dir}{name}
              </span>
            ) : (
              <>
                {dir ? (
                  <span className="min-w-0 shrink truncate text-slate-500 dark:text-zinc-400">
                    {dir}
                  </span>
                ) : null}
                <span className="min-w-0 max-w-full shrink-0 truncate font-semibold text-zinc-950 dark:text-zinc-50">
                  {name}
                </span>
              </>
            )}
          </h3>
        </div>
        <div className="flex shrink-0 items-center justify-between gap-2 pl-6 sm:justify-end sm:pl-0">
          <div className="flex items-center gap-2">
            {status === "loaded" && (file.additions > 0 || file.deletions > 0) ? (
              <div className="flex items-center gap-1.5 font-mono text-xs tabular-nums">
                {file.additions > 0 ? (
                  <span className="font-medium text-emerald-700 dark:text-emerald-300">
                    +{file.additions}
                  </span>
                ) : null}
                {file.deletions > 0 ? (
                  <span className="font-medium text-rose-700 dark:text-rose-300">
                    -{file.deletions}
                  </span>
                ) : null}
              </div>
            ) : null}
            {generated ? (
              <span
                className="rounded border border-slate-300/80 bg-slate-100 px-1.5 py-0.5 text-[11px] font-medium text-slate-600 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-300"
                title="Written by a tool, so it starts collapsed"
              >
                Generated
              </span>
            ) : null}
          </div>
          <div className="flex items-center gap-1.5">
            {onViewFile ? (
              <button
                className="rounded-md px-2 py-1 text-xs font-medium text-slate-600 transition hover:bg-slate-200/70 hover:text-zinc-950 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
                onClick={() => onViewFile({ ...file, changeKind: fileState?.changeKind ?? file.changeKind, oldPath: file.oldPath ?? fileState?.oldPath })}
                title="Read the whole file, before or after this change"
                type="button"
              >
                View file
              </button>
            ) : null}
            {onToggleViewed && status === "loaded" ? (
              <label
                className={cn(
                  "inline-flex min-h-7 cursor-pointer select-none items-center gap-1.5 rounded-md px-2 text-xs font-medium transition",
                  viewed
                    ? "bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-200"
                    : "text-slate-600 hover:bg-slate-200/70 hover:text-zinc-950 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
                )}
                title="Mark this file as reviewed (v). It stays marked until the file changes."
              >
                <input
                  aria-label={`Viewed ${file.path}`}
                  checked={viewed}
                  className="size-3.5 accent-emerald-600"
                  onChange={onToggleViewed}
                  type="checkbox"
                />
                Viewed
              </label>
            ) : null}
          </div>
        </div>
      </div>
      <div id={`${file.id}-body`}>
        {collapsed && status !== "error" ? null : status !== "loaded" ? (
          <PendingDiffBody
            error={fileState?.error}
            onRetry={onRetry ? () => onRetry(file.path) : undefined}
            status={status}
          />
        ) : isLargeDiff && !showLargeDiff ? (
          <LargeDiffGuard
            lineCount={changedLineCount}
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
      </div>
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
        <span>{getErrorMessage(error, "Request failed.")}</span>
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
      <span>Large diff ({lineCount} changed lines)</span>
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
