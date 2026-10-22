import { Link } from "@tanstack/react-router";

import type { Commit } from "../../api/types";
import { cn } from "../../lib/cn";
import { shortChangesetId, shortHash } from "../../lib/objectId";
import { Timestamp } from "../Timestamp";
import type { BlameBand, BlameLine } from "./blame";

// BlameGutter is the column left of a file's lines that says what last changed
// each one. It lines up with the code by sharing its line height (24px) and
// top padding. A run of lines from one commit reads as a block: its first line
// carries the commit's subject and date, its second the author and the
// change's order among those still in the file. A bar down every line is
// darker the newer its change, so recent lines stand out at a glance.
export function BlameGutter({
  filePath,
  lines,
  onLeave,
  onSelect,
  selected,
}: {
  filePath: string;
  lines: BlameLine[];
  // Called as a link to a changeset is followed.
  onLeave?(): void;
  // A tap on a block selects its commit, for the details a phone has no room
  // to show inline (see BlameDetails); a second tap clears it.
  onSelect?(commitId: string): void;
  selected?: string;
}) {
  // The changeset's page opens on this file's diff, as the history drawer does.
  const focusFile = filePath.replace(/^\/+/, "");
  return (
    <div
      aria-label="Blame"
      className="sticky left-0 z-10 w-20 shrink-0 select-none border-r border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 py-4 text-xs leading-6 sm:w-96"
      role="rowgroup"
    >
      {lines.map((line, index) => (
        <div
          className={cn(
            "relative flex h-6 items-center gap-2 overflow-hidden whitespace-nowrap pl-4 pr-3",
            line.run % 2 === 1 && "bg-slate-50 dark:bg-zinc-800/50",
            line.first &&
              index > 0 &&
              "border-t border-slate-200 dark:border-zinc-700",
            selected && line.commitId === selected && "bg-amber-100/80 dark:bg-amber-900/30",
            onSelect && line.commitId && "cursor-pointer",
          )}
          key={index}
          onClick={onSelect && line.commitId ? () => onSelect(line.commitId) : undefined}
          role="row"
        >
          {line.order ? (
            <span
              aria-hidden="true"
              className="absolute inset-y-0 left-0 w-1 bg-amber-500 dark:bg-amber-400"
              data-age={line.order}
              style={{ opacity: ageOpacity(line.order, line.total) }}
            />
          ) : null}
          {line.offset === 0 && line.first ? (
            <BlameHeadline commit={line.commit} commitId={line.commitId} focusFile={focusFile} line={line} onLeave={onLeave} />
          ) : line.offset === 1 ? (
            <BlameByline commit={line.commit} line={line} />
          ) : null}
        </div>
      ))}
    </div>
  );
}

// BlameHighlight marks the selected change on the code itself: a band over
// each stretch of lines it changed, across the width of the code. It shares the
// gutter's line height (24px) and top padding (16px), and lets clicks through.
export function BlameHighlight({ bands }: { bands: BlameBand[] }) {
  return (
    <div aria-hidden="true" className="pointer-events-none absolute inset-0" data-blame-highlight="">
      {bands.map((band) => (
        <div
          className="absolute inset-x-0 border-l-2 border-amber-500 bg-amber-300/25 dark:border-amber-400 dark:bg-amber-400/15"
          data-lines={`${band.start + 1}-${band.end + 1}`}
          key={band.start}
          style={{ height: (band.end - band.start + 1) * 24, top: 16 + band.start * 24 }}
        />
      ))}
    </div>
  );
}

// ageOpacity is how strongly a line's bar shows: faint for the oldest change
// still in the file, full for the newest.
export function ageOpacity(order: number, total: number): number {
  if (total <= 1) {
    return 1;
  }
  return 0.15 + (0.85 * (order - 1)) / (total - 1);
}

// orderLabel says where a change falls among those still in the file.
export function orderLabel(order: number, total: number): string {
  if (!order || !total) {
    return "";
  }
  if (total === 1) {
    return "the only change still in this file";
  }
  if (order === total) {
    return `the newest of ${total} changes still in this file`;
  }
  if (order === 1) {
    return `the oldest of ${total} changes still in this file`;
  }
  return `change ${order} of ${total} still in this file, oldest first`;
}

function BlameHeadline({
  commit,
  commitId,
  focusFile,
  line,
  onLeave,
}: {
  commit?: Commit;
  commitId: string;
  focusFile: string;
  line: BlameLine;
  onLeave?(): void;
}) {
  const handle = shortChangesetId(commit?.changesetId ?? "");
  const summary = commit?.message?.split("\n")[0]?.trim() ?? "";
  const label = shortHash(commitId, 8);
  const order = orderLabel(line.order, line.total);
  const title =
    [summary, commit?.author, order].filter(Boolean).join(" · ") || undefined;
  return (
    <>
      {handle ? (
        <Link
          className="hidden shrink-0 font-mono text-sky-700 underline-offset-2 hover:underline sm:inline dark:text-sky-300"
          onClick={(event) => {
            // The row's tap selects the change; following the link leaves.
            event.stopPropagation();
            onLeave?.();
          }}
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
          className="hidden shrink-0 font-mono text-slate-500 sm:inline dark:text-zinc-400"
          title={title}
        >
          {label}
        </span>
      )}
      {summary ? (
        <span
          className="hidden min-w-0 flex-1 truncate text-zinc-800 sm:inline dark:text-zinc-200"
          title={title}
        >
          {summary}
        </span>
      ) : (
        <span className="hidden flex-1 sm:inline" />
      )}
      <Timestamp
        className="min-w-0 shrink-0 truncate text-slate-500 dark:text-zinc-400"
        dateOnly
        value={commit?.createdAt}
      />
    </>
  );
}

function BlameByline({ commit, line }: { commit?: Commit; line: BlameLine }) {
  const order = orderLabel(line.order, line.total);
  return (
    <>
      <span className="hidden min-w-0 flex-1 truncate text-slate-500 sm:inline dark:text-zinc-400">
        {commit?.author ?? ""}
      </span>
      {line.order ? (
        <span
          className="ml-auto shrink-0 font-mono text-[11px] text-slate-400 dark:text-zinc-500"
          title={order}
        >
          {line.order}/{line.total}
        </span>
      ) : null}
    </>
  );
}

// BlameDetails is the selected block's commit in full, for phones, where the
// gutter has room only for dates. It is a sheet at the bottom of the screen,
// so it shows wherever in a long file the tap was: subject, author, date, its order among the
// changes still in the file, and the changeset that landed it.
export function BlameDetails({
  filePath,
  line,
  onClose,
  onLeave,
}: {
  filePath: string;
  line: BlameLine;
  onClose(): void;
  onLeave?(): void;
}) {
  const commit = line.commit;
  const handle = shortChangesetId(commit?.changesetId ?? "");
  const summary = commit?.message?.split("\n")[0]?.trim() || "No message";
  const focusFile = filePath.replace(/^\/+/, "");
  return (
    <div
      aria-label="Change"
      className="fixed inset-x-0 bottom-0 z-40 flex items-start gap-3 border-t border-amber-200 bg-amber-50 px-4 pb-[max(0.75rem,env(safe-area-inset-bottom))] pt-3 text-xs shadow-[0_-4px_16px_rgba(0,0,0,0.08)] dark:border-amber-900/60 dark:bg-zinc-900 sm:hidden"
      role="region"
    >
      <div className="min-w-0 flex-1">
        <p className="break-words text-sm font-medium text-zinc-950 dark:text-zinc-50">{summary}</p>
        <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-slate-600 dark:text-zinc-400">
          {handle ? (
            <Link
              className="font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
              onClick={() => onLeave?.()}
              params={{ id: handle }}
              search={(focusFile ? { file: focusFile } : {}) as never}
              to="/cs/$id"
            >
              {shortHash(line.commitId, 8)}
            </Link>
          ) : (
            <span className="font-mono">{shortHash(line.commitId, 8)}</span>
          )}
          {commit?.author ? <span>{commit.author}</span> : null}
          <Timestamp dateOnly value={commit?.createdAt} />
          {line.order ? <span>{orderLabel(line.order, line.total)}</span> : null}
        </p>
      </div>
      <button
        aria-label="Close"
        className="shrink-0 rounded px-1.5 text-base leading-5 text-slate-500 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-50"
        onClick={onClose}
        type="button"
      >
        ×
      </button>
    </div>
  );
}
