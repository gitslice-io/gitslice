import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";

import type { SliceRef } from "../../api/types";
import { useApi } from "../../api/useApi";
import { BlameDetails, BlameGutter } from "./BlameGutter";
import { blameLines, codeLineCount } from "./blame";

interface BlameViewProps {
  children: ReactNode;
  code: string;
  fill: boolean;
  path: string;
  repo: { commitId: string; slice: SliceRef };
}

const NOTE = "border-b border-slate-200 dark:border-zinc-800 px-4 py-2 text-xs";

// BlameView shows a file's lines (children) with who last changed each beside
// them. It asks the server for the blame of the file at the head, so it must
// match the text on screen: when the file has moved on since the page loaded,
// the lines no longer line up and the view says so instead of guessing.
export function BlameView({ children, code, fill, path, repo }: BlameViewProps) {
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
  const [selected, setSelected] = useState("");
  const selectedLine = selected ? lines?.find((line) => line.first && line.commitId === selected) : undefined;

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
      {selectedLine ? <BlameDetails filePath={path} line={selectedLine} onClose={() => setSelected("")} /> : null}
      <div className={fill ? "overflow-x-auto" : "max-h-[82dvh] overflow-auto"}>
        <div className="flex min-w-max">
          {lines ? (
            <BlameGutter
              filePath={path}
              lines={lines}
              onSelect={(commitId) => setSelected((current) => (current === commitId ? "" : commitId))}
              selected={selectedLine ? selected : undefined}
            />
          ) : null}
          <div className="min-w-0 flex-1">{children}</div>
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
