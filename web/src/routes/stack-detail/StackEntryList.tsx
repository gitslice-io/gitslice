import type { ChangesetStackEntry } from "../../api/types";
import { Link } from "@tanstack/react-router";
import { cn } from "../../lib/cn";
import {
  childCountMap,
  changedPathCount,
  currentPatchsetNumber,
  displaySubmitBlockedReason,
  entryByChangesetId,
  entryDepth,
  entryLabel,
  entryTitle,
  parentEntry,
  StackStatusBadge
} from "../stackPageUtils";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { shortChangesetId } from "../../lib/objectId";

export function StackEntryList({
  entries,
  onSelect,
  selectedEntryId,
  stackId
}: {
  entries: ChangesetStackEntry[];
  onSelect(entryId: string): void;
  selectedEntryId: string;
  stackId: string;
}) {
  const childCounts = childCountMap(entries);

  if (!entries.length) {
    return (
      <SliceNotice title="No dependent changesets yet">
        Use the add changeset form to create the root changeset.
      </SliceNotice>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-4 py-3">
        <h2 className="text-sm font-semibold text-zinc-950">Changesets</h2>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-3 py-3 sm:px-4">Changeset</th>
              <th className="hidden px-4 py-3 sm:table-cell">State</th>
              <th className="hidden px-4 py-3 md:table-cell">Patchset</th>
              <th className="hidden px-4 py-3 lg:table-cell">Base</th>
              <th className="hidden px-4 py-3 lg:table-cell">Dependents</th>
              <th className="hidden px-4 py-3 xl:table-cell">Paths</th>
              <th className="px-3 py-3 text-right sm:px-4">Open</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            {entries.map((entry) => {
              const changeset = entry.changeset;
              const selected = entry.changesetId === selectedEntryId;
              const patchsetNumber = currentPatchsetNumber(changeset);
              const parent = parentEntry(entries, entry);
              const depth = Math.min(entryDepth(entry), 8);

              return (
                <tr
                  className={cn(
                    "align-top transition hover:bg-slate-50",
                    selected && "bg-slate-50"
                  )}
                  key={entry.changesetId}
                >
                  <td className="max-w-[18rem] px-3 py-4 sm:max-w-none sm:px-4">
                    <button
                      className="group block w-full min-w-0 text-left"
                      onClick={() => onSelect(entry.changesetId || "")}
                      style={{ paddingLeft: `${depth * 1.25}rem` }}
                      type="button"
                    >
                      <span className="block break-words font-semibold text-zinc-950 underline decoration-slate-300 underline-offset-4 group-hover:decoration-slate-700">
                        {entryLabel(entry)}
                      </span>
                      <span className="mt-1 block break-words text-sm text-slate-700">
                        {entryTitle(entry)}
                      </span>
                      {changeset?.submitBlockedReason ? (
                        <span className="mt-2 block rounded-md border border-amber-200 bg-amber-50 px-2 py-1 text-xs text-amber-900 lg:hidden">
                          {displaySubmitBlockedReason(changeset.submitBlockedReason)}
                        </span>
                      ) : null}
                    </button>
                  </td>
                  <td className="hidden px-4 py-4 sm:table-cell">
                    <StackStatusBadge status={entry.state || changeset?.status} />
                  </td>
                  <td className="hidden px-4 py-4 text-slate-700 md:table-cell">
                    {patchsetNumber ? `patchset ${patchsetNumber}` : "none"}
                  </td>
                  <td className="hidden px-4 py-4 text-slate-700 lg:table-cell">
                    {parent ? entryLabel(parent) : "root"}
                  </td>
                  <td className="hidden px-4 py-4 text-slate-700 lg:table-cell">
                    {childCounts.get(entry.changesetId || "") ?? 0}
                  </td>
                  <td className="hidden px-4 py-4 text-slate-700 xl:table-cell">
                    {changedPathCount(changeset)}
                  </td>
                  <td className="px-3 py-4 sm:px-4">
                    <div className="flex flex-wrap justify-end gap-2">
                      <Link
                        className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 transition hover:bg-slate-50"
                        params={{
                          id:
                            shortChangesetId(entry.changesetId || "") ||
                            entry.changesetId ||
                            ""
                        }}
                        search={{ dependency: stackId } as never}
                        to="/cs/$id"
                      >
                        Detail
                      </Link>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}