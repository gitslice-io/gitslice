import { cn } from "../../lib/cn";
import { type FileChangeKind } from "./parseDiff";

export function ChangeKindBadge({ kind }: { kind: FileChangeKind }) {
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

function changeKindClass(kind: FileChangeKind) {
  switch (kind) {
    case "added":
      return "bg-emerald-100 dark:bg-emerald-950/45 text-emerald-800 dark:text-emerald-200";
    case "deleted":
      return "bg-rose-100 dark:bg-rose-950/45 text-rose-800 dark:text-rose-200";
    case "renamed":
      return "bg-violet-100 text-violet-800";
    case "modified":
      return "bg-amber-100 dark:bg-amber-950/45 text-amber-900 dark:text-amber-200";
    case "pending":
      return "bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400";
  }
}
