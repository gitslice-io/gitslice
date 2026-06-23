import type { Changeset } from "../../api/types";
import { Link } from "@tanstack/react-router";

export function ChangesetMetaLine({ changeset }: { changeset: Changeset }) {
  const author = changeset.author;
  if (!author) {
    return null;
  }

  return (
    <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11px] text-slate-500 md:mt-1.5 md:text-xs">
      <span className="shrink-0">by</span>
      <Link
        className="min-w-0 truncate font-medium text-slate-700 underline-offset-2 transition hover:text-zinc-950 hover:underline"
        search={{ account: author }}
        title={`View ${author}'s slices`}
        to="/slices"
      >
        {author}
      </Link>
    </div>
  );
}