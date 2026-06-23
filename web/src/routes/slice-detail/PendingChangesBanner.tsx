import { Link } from "@tanstack/react-router";

import { shortChangesetId } from "../../lib/objectId";

interface PendingChangesBannerProps {
  changesetRef: string;
  count: number;
  saveStatus: string;
}

export function PendingChangesBanner({
  changesetRef,
  count,
  saveStatus
}: PendingChangesBannerProps) {
  const label = `${count} pending ${count === 1 ? "change" : "changes"}`;
  const preparing = saveStatus === "saving" || saveStatus === "adopting";

  return (
    <div className="mt-4 flex flex-wrap items-center justify-between gap-3 rounded-md border border-amber-200 bg-amber-50 px-4 py-3">
      <div className="flex items-center gap-2 text-sm text-amber-900">
        <span aria-hidden="true" className="h-2 w-2 rounded-full bg-amber-500" />
        <span className="font-semibold">{label}</span>
        {saveStatus === "failed" ? (
          <span className="text-rose-700">— could not save draft</span>
        ) : null}
      </div>
      {changesetRef ? (
        <Link
          className="rounded-md border border-amber-300 bg-white px-3 py-1.5 text-xs font-semibold text-amber-900 transition hover:bg-amber-100 active:scale-[0.98]"
          params={{ id: changesetRef }}
          to="/cs/$id"
        >
          Review changeset →
        </Link>
      ) : (
        <span className="text-xs font-medium text-amber-800">
          {preparing ? "Saving…" : "Preparing…"}
        </span>
      )}
    </div>
  );
}