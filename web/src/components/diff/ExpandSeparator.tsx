import { cn } from "../../lib/cn";

import { type ExpandFn, EXPAND_CHUNK } from "./diffViewShared";

// A full-width separator standing in for a collapsed run of unchanged lines, with
// controls to reveal a chunk from the top, the whole gap, or a chunk from the
// bottom. Rendered with `flex` so it works inside both the unified <pre><code>
// and the split <div> bodies.
export function ExpandSeparator({
  className,
  gap,
  keyFor,
  onExpand
}: {
  className?: string;
  gap: { hiddenTotal: number; remaining: number };
  keyFor(): string;
  onExpand: ExpandFn;
}) {
  return (
    <span
      className={cn(
        "flex w-full items-center justify-center gap-3 border-y border-slate-100 dark:border-zinc-800 bg-sky-50/70 px-4 py-1 font-mono text-xs text-sky-700 dark:text-sky-300 first:border-t-0",
        className
      )}
    >
      <button
        aria-label={`Expand ${EXPAND_CHUNK} lines up`}
        className="rounded px-1.5 hover:bg-sky-100 dark:hover:bg-sky-950/45"
        onClick={() => onExpand(keyFor(), "up", gap.hiddenTotal)}
        type="button"
      >
        ↑
      </button>
      <button
        aria-label="Expand all hidden lines"
        className="rounded px-2 hover:bg-sky-100 dark:hover:bg-sky-950/45 hover:underline"
        onClick={() => onExpand(keyFor(), "all", gap.hiddenTotal)}
        type="button"
      >
        ⋯ {gap.remaining} hidden {gap.remaining === 1 ? "line" : "lines"}
      </button>
      <button
        aria-label={`Expand ${EXPAND_CHUNK} lines down`}
        className="rounded px-1.5 hover:bg-sky-100 dark:hover:bg-sky-950/45"
        onClick={() => onExpand(keyFor(), "down", gap.hiddenTotal)}
        type="button"
      >
        ↓
      </button>
    </span>
  );
}
