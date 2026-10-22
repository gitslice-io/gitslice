import { useQuery } from "@tanstack/react-query";
import { useLayoutEffect, useMemo, useState, type ReactNode } from "react";

import type { SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { BlameDetails, BlameGutter, BlameHighlight } from "./BlameGutter";
import { blameLines, codeLineCount, commitBands } from "./blame";

interface BlameViewProps {
  children: ReactNode;
  code: string;
  fill: boolean;
  path: string;
  repo: { commitId: string; slice: SliceRef };
  // The selected change, kept by the page (in the URL) so it survives a visit
  // to the commit's changeset and back. Without it the view keeps it itself.
  selection?: { at: string; onChange(at: string): void };
}

// shortCommitKey is the part of a commit id the URL carries: enough to name it
// among a file's few hundred changes.
export function shortCommitKey(commitId: string): string {
  return commitId.replace(/^sha256:/, "").slice(0, 12);
}

// The scroll position is kept for the address it was left at, so coming back
// to that address (the browser's back, after opening a changeset) restores it
// once the blame is drawn and the page is tall enough again.
const SCROLL_KEY = "gitslice.blame.scroll";

function saveScroll() {
  try {
    sessionStorage.setItem(SCROLL_KEY, JSON.stringify({ href: window.location.href, y: window.scrollY }));
  } catch {
    // Storage can be off (a private window): the page opens at the top.
  }
}

function restoreScroll() {
  try {
    const saved = JSON.parse(sessionStorage.getItem(SCROLL_KEY) ?? "null") as { href?: string; y?: number } | null;
    if (saved && saved.href === window.location.href && typeof saved.y === "number") {
      sessionStorage.removeItem(SCROLL_KEY);
      window.scrollTo(0, saved.y);
    }
  } catch {
    // As above.
  }
}

const NOTE = "border-b border-slate-200 dark:border-zinc-800 px-4 py-2 text-xs";

// BlameView shows a file's lines (children) with who last changed each beside
// them. It asks the server for the blame of the file at the head, so it must
// match the text on screen: when the file has moved on since the page loaded,
// the lines no longer line up and the view says so instead of guessing.
export function BlameView({ children, code, fill, path, repo, selection }: BlameViewProps) {
  const api = useApi();
  const query = useQuery({
    queryKey: ["blame", repo.slice.account, repo.slice.slice, repo.commitId, path],
    queryFn: () => api.blameFile({ path, slice: repo.slice }),
    staleTime: 60_000
  });
  const lineCount = useMemo(() => codeLineCount(code), [code]);
  const blame = query.data;
  const matches = blame !== undefined && (blame.lineCount ?? 0) === lineCount;
  const lines = useMemo(() => (blame && matches ? blameLines(blame, lineCount) : null), [blame, matches, lineCount]);
  const [localAt, setLocalAt] = useState("");
  const at = selection ? selection.at : localAt;
  const setAt = selection ? selection.onChange : setLocalAt;
  // The commit the short id in `at` names, among those the blame shows.
  const selected = useMemo(
    () => (at && lines ? (lines.find((line) => line.commitId && shortCommitKey(line.commitId).startsWith(at))?.commitId ?? "") : ""),
    [at, lines]
  );
  const selectedLine = selected ? lines?.find((line) => line.first && line.commitId === selected) : undefined;
  // Back from a commit's changeset, the page is where it was left.
  const rememberScroll = () => saveScroll();
  useLayoutEffect(() => {
    if (lines) {
      restoreScroll();
    }
  }, [lines]);
  const bands = useMemo(() => (lines && selectedLine ? commitBands(lines, selected) : []), [lines, selectedLine, selected]);

  return (
    <>
      {query.isPending ? <div className={`${NOTE} text-slate-400 dark:text-zinc-500`}>Loading blame…</div> : null}
      {query.isError ? (
        <div className={`${NOTE} text-amber-700 dark:text-amber-300`} role="alert">
          Blame unavailable: {query.error instanceof Error ? query.error.message : "could not load it"}
        </div>
      ) : null}
      {blame && !matches ? (
        <div className={`${NOTE} text-amber-700 dark:text-amber-300`} role="alert">
          This file changed since the page loaded, so its lines no longer match the blame. Reload the page to see it.
        </div>
      ) : null}
      {selectedLine ? <BlameDetails filePath={path} line={selectedLine} onClose={() => setAt("")} onLeave={rememberScroll} /> : null}
      <div className={fill ? "overflow-x-auto" : "max-h-[82dvh] overflow-auto"}>
        <div className="flex min-w-max">
          {lines ? (
            <BlameGutter
              filePath={path}
              lines={lines}
              onLeave={rememberScroll}
              onSelect={(commitId) => setAt(selected === commitId ? "" : shortCommitKey(commitId))}
              selected={selectedLine ? selected : undefined}
            />
          ) : null}
          <div className="relative min-w-0 flex-1">
            {children}
            {bands.length > 0 ? <BlameHighlight bands={bands} /> : null}
          </div>
        </div>
      </div>
      {lines && blame?.truncated ? (
        <div className="border-t border-slate-200 dark:border-zinc-800 px-4 py-2 text-xs text-slate-500 dark:text-zinc-400">
          This file has a long history: lines older than the newest few hundred commits are credited to the oldest commit read.
        </div>
      ) : null}
    </>
  );
}
