import { Fragment, useMemo } from "react";
import { cn } from "../../lib/cn";
import { type RevealState } from "./collapse";
import { type DiffFile, type DiffLine, type DiffRow } from "./parseDiff";

import {
  type ExpandFn,
  gapKey,
  rangeIndexes,
  splitSegments
} from "./diffViewShared";
import { ExpandSeparator } from "./ExpandSeparator";

export function SplitDiff({
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
    () => splitSegments(file.id, rows, revealed),
    [file.id, rows, revealed]
  );

  if (rows.length === 0) {
    return (
      <div className="bg-white dark:bg-zinc-900 px-4 py-5 text-sm text-slate-500 dark:text-zinc-400">
        No line changes in this file.
      </div>
    );
  }

  return (
    <div className="bg-white dark:bg-zinc-900 text-xs leading-4 md:text-[13px]">
      <div className="grid grid-cols-[2rem_minmax(0,1fr)_2rem_minmax(0,1fr)] py-1.5 sm:grid-cols-[3.5rem_minmax(0,1fr)_3.5rem_minmax(0,1fr)]">
        {segments.map((segment) =>
          segment.type === "gap" ? (
            <ExpandSeparator
              className="col-span-full"
              gap={segment}
              key={`gap-${segment.index}`}
              keyFor={() => gapKey(file.id, "split", segment.index)}
              onExpand={onExpand}
            />
          ) : (
            <Fragment key={`block-${segment.start}`}>
              {rangeIndexes(segment.start, segment.end).map((index) => (
                <SplitRow key={index} row={rows[index]} />
              ))}
            </Fragment>
          )
        )}
      </div>
    </div>
  );
}

// Renders one split-view row as bare grid cells (a fragment, not a wrapper) so
// every cell is a direct child of the single shared grid in SplitDiff. The two
// code columns split the panel width evenly and long lines wrap inside them, so
// the two sides of a row stay the same height and nothing scrolls sideways —
// subgrid did not align rows reliably on mobile Safari.
function SplitRow({ row }: { row: DiffRow }) {
  if (row.kind === "hunk" || row.kind === "meta") {
    return (
      <div
        className={cn(
          "col-span-full whitespace-pre-wrap border-t border-slate-100 px-4 py-0.5 font-mono [overflow-wrap:anywhere] dark:border-zinc-800",
          row.kind === "hunk" && row.hunkText?.startsWith("@@")
            ? "bg-sky-50 dark:bg-sky-950/30 text-sky-700 dark:text-sky-300"
            : "bg-slate-50 dark:bg-zinc-950 text-slate-500 dark:text-zinc-400"
        )}
      >
        {row.hunkText}
      </div>
    );
  }

  return (
    <>
      <SplitCell
        line={row.left}
        tone={row.kind === "del" || row.kind === "replace" ? "del" : "context"}
      />
      <SplitCell
        line={row.right}
        tone={row.kind === "add" || row.kind === "replace" ? "add" : "context"}
      />
    </>
  );
}

function SplitCell({
  line,
  tone
}: {
  line?: DiffLine;
  tone: "add" | "context" | "del";
}) {
  const rowBorder = "border-t border-slate-100 dark:border-zinc-800";

  if (!line) {
    return (
      <div className={cn("col-span-2 min-h-4 bg-slate-50/80 dark:bg-zinc-950/80", rowBorder)} />
    );
  }

  const toneClass = cn(
    rowBorder,
    tone === "add" && "bg-emerald-50 dark:bg-emerald-950/30 text-emerald-900 dark:text-emerald-200",
    tone === "del" && "bg-rose-50 dark:bg-rose-950/30 text-rose-900 dark:text-rose-200",
    tone === "context" && "text-slate-700 dark:text-zinc-300"
  );

  return (
    <>
      <span
        className={cn(
          "min-h-4 select-none pl-2 pr-1 text-right text-[11px] text-slate-400 dark:text-zinc-500 sm:pl-4 sm:pr-3 sm:text-xs",
          toneClass
        )}
      >
        {tone === "add" ? line.newNumber : line.oldNumber}
      </span>
      <span
        className={cn(
          "min-h-4 min-w-0 whitespace-pre-wrap pr-2 font-mono [overflow-wrap:anywhere] sm:pr-4",
          toneClass
        )}
      >
        {line.content || " "}
      </span>
    </>
  );
}
