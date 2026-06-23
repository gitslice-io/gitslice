import type { UseQueryResult } from "@tanstack/react-query";
import type { ChangesetStack, ChangesetStackEntry, DiffChangesetResponse } from "../../api/types";
import { Link } from "@tanstack/react-router";
import { secondaryButtonClass } from "../stackPageUtils";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { DiffViewer } from "../../components/diff/DiffViewer";
import {
  changedPathCount,
  conflictCount,
  currentPatchset,
  currentPatchsetNumber,
  displaySubmitBlockedReason,
  entryByChangesetId,
  entryDepth,
  entryLabel,
  entryTitle,
  formatCommit,
  formatTimestamp,
  parentEntry
} from "../stackPageUtils";
import { Metadata } from "./Metadata";
import { shortChangesetId } from "../../lib/objectId";
import { EntryPreviewTree } from "./EntryPreviewTree";
import { StackStatusBadge } from "../stackPageUtils";

export function SelectedEntryDetail({
  diffQuery,
  entries,
  entry,
  stack
}: {
  diffQuery: UseQueryResult<DiffChangesetResponse, Error>;
  entries: ChangesetStackEntry[];
  entry: ChangesetStackEntry | null;
  stack: ChangesetStack;
}) {
  if (!entry) {
    return (
      <SliceNotice title="Select a changeset">
        Choose a changeset in the dependency tree to inspect metadata and diff.
      </SliceNotice>
    );
  }

  const changeset = entry.changeset;
  const parent = parentEntry(entries, entry);
  const patchset = currentPatchset(changeset);
  const changesetUrlId =
    shortChangesetId(entry.changesetId || "") || entry.changesetId || "";

  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="break-words text-lg font-semibold text-zinc-950">
                {entryTitle(entry)}
              </h2>
              <StackStatusBadge status={entry.state || changeset?.status} />
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 text-sm text-slate-600">
              <span className="break-all rounded-md bg-slate-100 px-2 py-1 font-mono text-xs text-slate-700">
                {entryLabel(entry)}
              </span>
              <span>{changeset?.author || "author not returned"}</span>
              <span>{patchset ? `patchset ${patchset.number}` : "no patchset"}</span>
            </div>
          </div>
          <Link
            className={secondaryButtonClass}
            params={{ id: changesetUrlId }}
            search={{ dependency: stack.id } as never}
            to="/cs/$id"
          >
            Open changeset
          </Link>
        </div>

        <dl className="mt-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <Metadata label="Base changeset" value={parent ? entryLabel(parent) : "root"} />
          <Metadata
            label="Base patchset"
            value={entry.parentPatchsetId || changeset?.parentPatchsetId || "none"}
          />
          <Metadata label="Depth" value={String(entryDepth(entry))} />
          <Metadata label="Changed paths" value={String(changedPathCount(changeset))} />
          <Metadata label="Base kind" value={changeset?.baseKind || patchset?.baseKind || "commit"} />
          <Metadata label="Base commit" value={formatCommit(changeset?.baseCommitId)} />
          <Metadata label="Conflicts" value={String(conflictCount(changeset))} />
          <Metadata
            label="Updated"
            value={formatTimestamp(stack.updatedAt || stack.createdAt)}
          />
        </dl>

        {changeset?.submitBlockedReason ? (
          <div className="mt-5 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900">
            {displaySubmitBlockedReason(changeset.submitBlockedReason)}
          </div>
        ) : null}
      </div>

      <EntryPreviewTree entry={entry} stack={stack} />

      <DiffViewer
        diffResponse={diffQuery.data}
        error={diffQuery.error}
        isError={diffQuery.isError}
        isLoading={diffQuery.isPending}
      />
    </div>
  );
}