import { cn } from "../../lib/cn";
import { type FileChangeKind } from "./parseDiff";

export function ChangeKindBadge({
  compact = false,
  kind
}: {
  compact?: boolean;
  kind: FileChangeKind;
}) {
  if (compact) {
    return (
      <span
        aria-label={kind}
        className={cn(
          "inline-flex h-5 min-w-5 shrink-0 items-center justify-center rounded px-1 font-mono text-[11px] font-bold uppercase leading-none",
          changeKindClass(kind)
        )}
        title={kind}
      >
        {shortKindLetter(kind)}
      </span>
    );
  }

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

function shortKindLetter(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "A";
    case "deleted":
      return "D";
    case "renamed":
      return "R";
    case "modified":
      return "M";
    case "pending":
      return "·";
  }
}

function changeKindClass(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "bg-emerald-100 dark:bg-emerald-950/45 text-emerald-800 dark:text-emerald-200";
    case "deleted":
      return "bg-rose-100 dark:bg-rose-950/45 text-rose-800 dark:text-rose-200";
    case "renamed":
      return "bg-violet-100 dark:bg-violet-950/45 text-violet-800 dark:text-violet-200";
    case "modified":
      return "bg-amber-100 dark:bg-amber-950/45 text-amber-900 dark:text-amber-200";
    case "pending":
      return "bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400";
  }
}
