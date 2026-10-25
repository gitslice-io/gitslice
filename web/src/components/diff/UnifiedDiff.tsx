import { Fragment, useMemo } from "react";
import { cn } from "../../lib/cn";
import { type RevealState } from "./collapse";
import { type DiffFile, type DiffLineKind } from "./parseDiff";

import {
  type ExpandFn,
  gapKey,
  rangeIndexes,
  unifiedSegments
} from "./diffViewShared";
import { ExpandSeparator } from "./ExpandSeparator";

export function UnifiedDiff({
  file,
  onExpand,
  revealed
}: {
  file: DiffFile;
  onExpand: ExpandFn;
  revealed: Record<string, RevealState>;
}) {
  const segments = useMemo(
    () => unifiedSegments(file.id, file.lines, revealed),
    [file.id, file.lines, revealed]
  );

  return (
    // Long lines wrap inside the viewport (no horizontal scrolling); a wrapped
    // line keeps its number on the first visual row.
    <pre className="bg-white dark:bg-zinc-900 text-xs leading-4 md:text-[13px]">
      <code className="block py-1.5">
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
                      "grid min-h-4 grid-cols-[2rem_minmax(0,1fr)] px-1.5 sm:grid-cols-[3.5rem_3.5rem_minmax(0,1fr)] sm:px-4",
                      diffLineClass(line.kind)
                    )}
                    key={`${index}-${line.text}`}
                  >
                    <span className="hidden select-none pr-1 text-right text-[11px] text-slate-400 dark:text-zinc-500 sm:block sm:pr-3 sm:text-xs">
                      {line.oldNumber ?? ""}
                    </span>
                    <span className="select-none pr-1 text-right text-[11px] text-slate-400 dark:text-zinc-500 sm:pr-3 sm:text-xs">
                      {line.newNumber ?? ""}
                    </span>
                    <span className="min-w-0 whitespace-pre-wrap [overflow-wrap:anywhere]">
                      {line.text || " "}
                    </span>
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

function diffLineClass(kind: DiffLineKind) {
  switch (kind) {
    case "add":
      return "bg-emerald-50 dark:bg-emerald-950/30 text-emerald-900 dark:text-emerald-200";
    case "del":
      return "bg-rose-50 dark:bg-rose-950/30 text-rose-900 dark:text-rose-200";
    case "hunk":
      return "bg-sky-50 dark:bg-sky-950/30 text-sky-700 dark:text-sky-300";
    case "meta":
      return "bg-slate-50 dark:bg-zinc-950 text-slate-500 dark:text-zinc-400";
    case "context":
      return "text-slate-700 dark:text-zinc-300";
  }
}
