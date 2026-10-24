import type { Changeset } from "../../api/types";

import { AuthorLabel } from "../../components/AccountAvatar";

export function ChangesetMetaLine({ changeset }: { changeset: Changeset }) {
  const author = changeset.author;
  if (!author) {
    return null;
  }

  return (
    <span className="inline-flex min-w-0 items-center gap-1 text-[11px] text-slate-500 dark:text-zinc-400 md:text-xs">
      <span className="shrink-0">by</span>
      <AuthorLabel
        className="font-medium text-slate-700 transition hover:text-zinc-950 dark:text-zinc-300 dark:hover:text-zinc-50"
        name={author}
      />
    </span>
  );
}
