import { useEffect, useMemo, useRef, useState } from "react";

import type { DiffChangesetResponse } from "../../api/types";
import { cn } from "../../lib/cn";
import { ChangedFilesTree } from "./ChangedFilesTree";
import {
  parseDiff,
  type DiffFile,
  type DiffLine,
  type DiffLineKind,
  type DiffRow,
  type FileChangeKind
} from "./parseDiff";

interface DiffViewerProps {
  diffResponse?: DiffChangesetResponse;
  error: unknown;
  isError: boolean;
  isLoading: boolean;
}

type ViewMode = "unified" | "split";

const diffViewStorageKey = "gitslice.diffView";

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
    if (!files.length) {
      setActiveId(undefined);
      return;
    }

    setActiveId((current) =>
      current && files.some((file) => file.id === current) ? current : files[0].id
    );
  }, [files]);

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
  }, [files]);

  const selectFile = (id: string) => {
    setActiveId(id);
    document.getElementById(id)?.scrollIntoView({
      behavior: "smooth",
      block: "start"
    });
  };

  return (
    <section className="mt-6 rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-5 py-4 md:px-6">
        <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div>
            <h2 className="text-base font-semibold tracking-normal text-zinc-950">
              Changed files
            </h2>
            <p className="mt-1 text-sm text-slate-600">
              {isLoading
                ? "Loading file changes..."
                : `${changedCount} file(s) changed`}
            </p>
            {!isLoading && files.length > 0 ? (
              <div className="mt-2 flex gap-2 font-mono text-xs">
                <span className="rounded bg-emerald-50 px-2 py-1 text-emerald-800">
                  +{totalAdditions}
                </span>
                <span className="rounded bg-rose-50 px-2 py-1 text-rose-800">
                  -{totalDeletions}
                </span>
              </div>
            ) : null}
          </div>
          <ViewModeToggle value={viewMode} onChange={setViewMode} />
        </div>
      </div>

      {isLoading ? (
        <DiffSkeleton />
      ) : isError ? (
        <div className="p-5 md:p-6">
          <DiffErrorBox message={errorMessage(error)} />
        </div>
      ) : hasTextualDiff ? (
        <div className="grid gap-4 p-4 md:p-5 lg:grid-cols-[18rem_minmax(0,1fr)]">
          <aside className="lg:sticky lg:top-20 lg:self-start">
            <ChangedFilesTree
              activeId={activeId}
              files={files}
              onSelect={selectFile}
            />
          </aside>
          <div className="grid min-w-0 gap-4">
            {files.map((file) => (
              <DiffFilePanel
                file={file}
                key={file.id}
                refCallback={(node) => {
                  panelRefs.current[file.id] = node;
                }}
                viewMode={viewMode}
              />
            ))}
          </div>
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
  refCallback,
  viewMode
}: {
  file: DiffFile;
  refCallback(node: HTMLElement | null): void;
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
      {viewMode === "unified" ? (
        <UnifiedDiff file={file} />
      ) : (
        <SplitDiff rows={file.rows} />
      )}
    </article>
  );
}

function UnifiedDiff({ file }: { file: DiffFile }) {
  return (
    <pre className="overflow-x-auto bg-white text-xs leading-5 md:text-sm">
      <code className="block min-w-max py-2">
        {file.lines.map((line, index) => (
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
        ))}
      </code>
    </pre>
  );
}

function SplitDiff({ rows }: { rows: DiffRow[] }) {
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
        {rows.map((row, index) =>
          row.kind === "hunk" ? (
            <div
              className={cn(
                "border-t border-slate-100 px-4 py-1 font-mono first:border-t-0",
                row.hunkText?.startsWith("@@")
                  ? "bg-sky-50 text-sky-700"
                  : "bg-slate-50 text-slate-500"
              )}
              key={`${index}-${row.hunkText}`}
            >
              {row.hunkText}
            </div>
          ) : (
            <div
              className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] border-t border-slate-100 first:border-t-0"
              key={splitRowKey(row, index)}
            >
              <SplitCell
                line={row.left}
                tone={
                  row.kind === "del" || row.kind === "replace"
                    ? "del"
                    : "context"
                }
              />
              <SplitCell
                line={row.right}
                tone={
                  row.kind === "add" || row.kind === "replace"
                    ? "add"
                    : "context"
                }
              />
            </div>
          )
        )}
      </div>
    </div>
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
