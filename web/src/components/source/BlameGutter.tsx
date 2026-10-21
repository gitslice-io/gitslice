import { Link } from "@tanstack/react-router";

import type { Commit } from "../../api/types";
import { cn } from "../../lib/cn";
import { shortChangesetId, shortHash } from "../../lib/objectId";
import { Timestamp } from "../Timestamp";
import type { BlameLine } from "./blame";

// BlameGutter is the column left of a file's lines that says who last changed
// each one. It lines up with the code by sharing its line height (24px) and
// top padding; a run of lines from one commit shows the commit once, on its
// first line, with a rule above it.
export function BlameGutter({
  filePath,
  lines,
}: {
  filePath: string;
  lines: BlameLine[];
}) {
  // The changeset's page opens on this file's diff, as the history drawer does.
  const focusFile = filePath.replace(/^\/+/, "");
  return (
    <div
      aria-label="Blame"
      className="sticky left-0 z-10 w-44 shrink-0 select-none border-r border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 py-4 text-xs leading-6 sm:w-72"
      role="rowgroup"
    >
      {lines.map((line, index) => (
        <div
          className={cn(
            "flex h-6 items-center gap-2 overflow-hidden whitespace-nowrap px-3",
            line.run % 2 === 1 && "bg-slate-50 dark:bg-zinc-800/50",
            line.first &&
              index > 0 &&
              "border-t border-slate-200 dark:border-zinc-700",
          )}
          key={index}
          role="row"
        >
          {line.first ? (
            <BlameDetails
              commit={line.commit}
              commitId={line.commitId}
              focusFile={focusFile}
            />
          ) : null}
        </div>
      ))}
    </div>
  );
}

function BlameDetails({
  commit,
  commitId,
  focusFile,
}: {
  commit?: Commit;
  commitId: string;
  focusFile: string;
}) {
  const handle = shortChangesetId(commit?.changesetId ?? "");
  const summary = commit?.message?.split("\n")[0] ?? "";
  const label = shortHash(commitId, 8);
  const title =
    [summary, commit?.author].filter(Boolean).join(" · ") || undefined;
  return (
    <>
      {handle ? (
        <Link
          className="shrink-0 font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
          params={{ id: handle }}
          search={(focusFile ? { file: focusFile } : {}) as never}
          title={
            title
              ? `${title}\nOpen changeset ${handle}`
              : `Open changeset ${handle}`
          }
          to="/cs/$id"
        >
          {label}
        </Link>
      ) : (
        <span
          className="shrink-0 font-mono text-slate-500 dark:text-zinc-400"
          title={title}
        >
          {label}
        </span>
      )}
      {commit?.author ? (
        <span className="hidden min-w-0 truncate text-slate-700 sm:inline dark:text-zinc-300">
          {commit.author}
        </span>
      ) : null}
      <Timestamp
        className="ml-auto shrink-0 text-slate-400 dark:text-zinc-500"
        relative
        value={commit?.createdAt}
      />
    </>
  );
}
