import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";

import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { FileTypeIcon } from "../../components/source/FileTypeIcon";
import { cn } from "../../lib/cn";
import { rankPaths } from "../../lib/fuzzy";
import { displayPath, listSliceFiles, sharedRoot } from "./sliceFiles";

const RESULT_LIMIT = 50;

interface FileFinderProps {
  api: ApiClient;
  commitId: string;
  includedPaths: readonly string[];
  onClose(): void;
  // Opens the file, by its canonical path.
  onSelectPath(path: string): void;
  slice: SliceRef;
}

// FileFinder is the "go to file" dialog: type part of a name and jump to the
// file. The slice's files are listed once, on first open, and matched in the
// browser as the person types.
export function FileFinder({ api, commitId, includedPaths, onClose, onSelectPath, slice }: FileFinderProps) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();
  const listRef = useRef<HTMLUListElement>(null);

  const files = useQuery({
    queryFn: () => listSliceFiles(api, slice, commitId, includedPaths),
    queryKey: ["sliceFiles", slice.account, slice.slice, commitId, includedPaths.join("|")],
    retry: 1,
    retryDelay: 400,
    // A commit's files never change.
    staleTime: Number.POSITIVE_INFINITY
  });

  const root = sharedRoot(includedPaths);
  // The finder shows and matches paths without the folder every file shares.
  const entries = useMemo(
    () => (files.data?.paths ?? []).map((path) => ({ display: displayPath(path, root), path })),
    [files.data?.paths, root]
  );
  const byDisplay = useMemo(() => new Map(entries.map((entry) => [entry.display, entry.path])), [entries]);

  const results = useMemo(() => {
    if (query.replace(/\s+/g, "") === "") {
      // Nothing typed: the shallowest files first, as an overview.
      return [...entries]
        .sort((a, b) => depth(a.display) - depth(b.display) || a.display.localeCompare(b.display))
        .slice(0, RESULT_LIMIT)
        .map((entry) => ({ display: entry.display, indexes: [] as number[] }));
    }
    return rankPaths(query, entries.map((entry) => entry.display), RESULT_LIMIT).map((match) => ({
      display: match.path,
      indexes: match.indexes
    }));
  }, [entries, query]);

  // A new query starts from the best match.
  useEffect(() => setActive(0), [query]);
  // Keep the chosen row in view as the arrows move through a long list.
  useEffect(() => {
    listRef.current?.children[active]?.scrollIntoView?.({ block: "nearest" });
  }, [active]);

  const open = (display: string) => {
    const path = byDisplay.get(display);
    if (path) {
      onSelectPath(path);
      onClose();
    }
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown" || (event.ctrlKey && event.key === "n")) {
      event.preventDefault();
      setActive((current) => (results.length ? (current + 1) % results.length : 0));
    } else if (event.key === "ArrowUp" || (event.ctrlKey && event.key === "p")) {
      event.preventDefault();
      setActive((current) => (results.length ? (current - 1 + results.length) % results.length : 0));
    } else if (event.key === "Enter") {
      event.preventDefault();
      if (results[active]) {
        open(results[active].display);
      }
    } else if (event.key === "Escape") {
      event.preventDefault();
      onClose();
    }
  };

  const typed = query.replace(/\s+/g, "") !== "";
  const total = entries.length;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-zinc-950/40 p-3 pt-[8vh] sm:p-4 sm:pt-[12vh]"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        aria-label="Find file"
        aria-modal="true"
        className="flex max-h-[80dvh] w-full max-w-xl flex-col overflow-hidden rounded-lg border border-slate-200 bg-white shadow-xl dark:border-zinc-700 dark:bg-zinc-900"
        role="dialog"
      >
        <div className="border-b border-slate-200 p-2 dark:border-zinc-800">
          <input
            aria-activedescendant={results[active] ? `${listId}-${active}` : undefined}
            aria-autocomplete="list"
            aria-controls={listId}
            aria-expanded="true"
            aria-label="Find a file by name"
            autoComplete="off"
            autoFocus
            className="h-10 w-full rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none focus:border-slate-500 dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-50"
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Go to file…"
            role="combobox"
            spellCheck={false}
            type="text"
            value={query}
          />
        </div>

        {files.isPending ? (
          <p className="px-4 py-6 text-sm text-slate-500 dark:text-zinc-400">Listing the slice&apos;s files…</p>
        ) : files.isError ? (
          <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-6 text-sm text-rose-800 dark:text-rose-200">
            <span>Could not list the files: {files.error instanceof Error ? files.error.message : "request failed"}</span>
            <button
              className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300"
              onClick={() => void files.refetch()}
              type="button"
            >
              Retry
            </button>
          </div>
        ) : results.length === 0 ? (
          <p className="px-4 py-6 text-sm text-slate-500 dark:text-zinc-400">
            {typed
              ? `No file matches “${query.trim()}”.`
              : files.data.partial
                ? "Could not list this slice's files."
                : "This slice has no files."}
          </p>
        ) : (
          <ul
            className="min-h-0 flex-1 overflow-y-auto py-1"
            id={listId}
            ref={listRef}
            role="listbox"
          >
            {results.map((result, index) => (
              <li
                aria-selected={index === active}
                className={cn(
                  "flex min-h-10 cursor-pointer items-center gap-2 px-3 py-1.5 text-sm",
                  index === active
                    ? "bg-slate-900 text-white dark:bg-zinc-100 dark:text-zinc-950"
                    : "text-slate-700 hover:bg-slate-50 dark:text-zinc-300 dark:hover:bg-zinc-800"
                )}
                id={`${listId}-${index}`}
                key={result.display}
                onClick={() => open(result.display)}
                onMouseMove={() => setActive(index)}
                role="option"
              >
                <FileTypeIcon name={result.display.slice(result.display.lastIndexOf("/") + 1)} />
                <span className="min-w-0 flex-1 truncate">
                  <PathLabel active={index === active} display={result.display} indexes={result.indexes} />
                </span>
              </li>
            ))}
          </ul>
        )}

        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-slate-200 px-3 py-2 text-xs text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
          <span>
            {files.data
              ? typed
                ? `${results.length === RESULT_LIMIT ? `First ${RESULT_LIMIT}` : results.length} of ${total} files`
                : `${total} ${total === 1 ? "file" : "files"}${files.data.partial ? " (not all listed)" : ""}`
              : ""}
            {files.data?.partial && typed ? " (not all listed)" : ""}
          </span>
          <span className="hidden sm:inline">↑↓ to move · ↵ to open · esc to close</span>
        </div>
      </div>
    </div>
  );
}

function depth(path: string) {
  return path.split("/").length;
}

// The folders of a path dimmed and its name in full, with the letters the
// query matched picked out.
function PathLabel({ active, display, indexes }: { active: boolean; display: string; indexes: number[] }) {
  const matched = new Set(indexes);
  const nameStart = display.lastIndexOf("/") + 1;
  return (
    <span title={display}>
      {nameStart > 0 ? (
        <span className={active ? "opacity-70" : "text-slate-500 dark:text-zinc-400"}>
          <Highlighted active={active} matched={matched} offset={0} text={display.slice(0, nameStart)} />
        </span>
      ) : null}
      <span className="font-medium">
        <Highlighted active={active} matched={matched} offset={nameStart} text={display.slice(nameStart)} />
      </span>
    </span>
  );
}

// text, with the characters whose position in the whole path (offset plus
// their own) is in matched set in bold and a color.
function Highlighted({ active, matched, offset, text }: { active: boolean; matched: Set<number>; offset: number; text: string }) {
  const runs: { matched: boolean; text: string }[] = [];
  for (let i = 0; i < text.length; i++) {
    const isMatch = matched.has(offset + i);
    const last = runs[runs.length - 1];
    if (last && last.matched === isMatch) {
      last.text += text[i];
    } else {
      runs.push({ matched: isMatch, text: text[i] });
    }
  }
  return (
    <>
      {runs.map((run, index) =>
        run.matched ? (
          <mark
            className={cn("bg-transparent font-bold", active ? "text-sky-300 dark:text-sky-700" : "text-sky-700 dark:text-sky-300")}
            key={index}
          >
            {run.text}
          </mark>
        ) : (
          <span key={index}>{run.text}</span>
        )
      )}
    </>
  );
}
