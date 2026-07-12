import type { Changeset } from "../../api/types";
import { Link } from "@tanstack/react-router";

export function ChangesetMetaLine({ changeset }: { changeset: Changeset }) {
  const author = changeset.author;
  if (!author) {
    return null;
  }

  return (
    <span className="inline-flex min-w-0 items-center gap-1 text-[11px] text-slate-500 dark:text-zinc-400 md:text-xs">
      <span className="shrink-0">by</span>
      <Link
        className="min-w-0 truncate font-medium text-slate-700 dark:text-zinc-300 underline-offset-2 transition hover:text-zinc-950 dark:hover:text-zinc-50 hover:underline"
        search={{ account: author }}
        title={`View ${author}'s slices`}
        to="/"
      >
        {author}
      </Link>
    </span>
  );
}
