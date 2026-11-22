import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import type { Changeset } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AuthorLabel } from "../../components/AccountAvatar";
import { shortChangesetId } from "../../lib/objectId";
import { ChangesetMetaLine } from "../changeset-detail/ChangesetMetaLine";
import { StatusBadge } from "../changeset-detail/StatusBadge";
import { mergeErrorMessage } from "../changeset-detail/status";
import { displaySubmitBlockedReason } from "../stackPageUtils";

// The key every variant of the list shares, to refresh them all at once.
export type ChangesetsQueryKey = readonly ["changesets", string, string];

export function ChangesetsTable({
  api,
  canManage,
  changesets,
  queryKey
}: {
  api: ApiClient;
  canManage: boolean;
  changesets: Changeset[];
  queryKey: ChangesetsQueryKey;
}) {
  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
          <thead className="hidden bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400 sm:table-header-group">
            <tr>
              <th className="px-3 py-3 sm:px-4">Changeset</th>
              <th className="hidden px-3 py-3 sm:table-cell sm:px-4">Status</th>
              <th className="hidden px-4 py-3 md:table-cell">Author</th>
              <th className="hidden px-4 py-3 md:table-cell">Approvals</th>
              <th className="hidden px-4 py-3 lg:table-cell">Checks</th>
              <th className="hidden px-4 py-3 md:table-cell">
                Blocked reason
              </th>
              {canManage ? (
                <th className="px-3 py-3 text-right sm:px-4">Actions</th>
              ) : null}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
            {changesets.map((changeset) => (
              <ChangesetRow
                api={api}
                canManage={canManage}
                changeset={changeset}
                key={changeset.id || changeset.number}
                queryKey={queryKey}
              />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function ChangesetRow({
  api,
  canManage,
  changeset,
  queryKey
}: {
  api: ApiClient;
  canManage: boolean;
  changeset: Changeset;
  queryKey: ChangesetsQueryKey;
}) {
  const queryClient = useQueryClient();
  const changesetId = changeset.id ?? "";
  const detailId = shortChangesetId(changesetId) || changesetId;
  const label = changesetLabel(changeset);
  const mergeable = isMergeableStatus(changeset.status);
  const requiredChecks = changeset.submitRequirements?.requiredChecks ?? [];
  const [rowError, setRowError] = useState("");

  const invalidateList = async () => {
    await queryClient.invalidateQueries({ queryKey });
  };

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!changesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.submitChangeset({
        changesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setRowError(mergeErrorMessage(error)),
    onMutate: () => setRowError(""),
    onSuccess: async () => {
      setRowError("");
      await invalidateList();
    }
  });

  const busy = mergeMutation.isPending;

  return (
    <tr className="align-top transition hover:bg-slate-50 dark:hover:bg-zinc-950">
      <td className="px-3 py-3.5 sm:min-w-72 sm:px-4 sm:py-4">
        <div className="flex flex-col gap-1">
          <div className="sm:hidden">
            <StatusBadge status={changeset.status} />
          </div>
          {detailId ? (
            <Link
              className="group min-w-0"
              params={{ id: detailId }}
              to="/cs/$id"
            >
              <span className="block max-w-xl line-clamp-2 break-words font-semibold text-zinc-950 dark:text-zinc-50 underline-offset-2 group-hover:text-sky-700 group-hover:underline dark:group-hover:text-sky-300">
                {changeset.title || "Untitled changeset"}
              </span>
              <span className="mt-1 block break-words text-sm text-slate-500 dark:text-zinc-400">
                {label}
              </span>
            </Link>
          ) : (
            <>
              <span className="line-clamp-2 max-w-xl break-words font-semibold text-zinc-950 dark:text-zinc-50">
                {changeset.title || "Untitled changeset"}
              </span>
              <span className="mt-1 text-sm text-slate-500 dark:text-zinc-400">{label}</span>
            </>
          )}
          {/* The Author column shows from md up; below it the name sits here. */}
          <div className="md:hidden">
            <ChangesetMetaLine changeset={changeset} />
          </div>
          {changeset.affectedPaths?.length ? (
            <span
              className="text-xs text-slate-500 dark:text-zinc-400"
              title={changeset.affectedPaths.join("\n")}
            >
              {changeset.affectedPaths.length}{" "}
              {changeset.affectedPaths.length === 1 ? "file" : "files"} changed
            </span>
          ) : null}
          {changeset.submitBlockedReason ? (
            <p className="mt-2 rounded-md border border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 px-2 py-1 text-xs leading-5 text-amber-900 dark:text-amber-200 md:hidden">
              {displaySubmitBlockedReason(changeset.submitBlockedReason)}
            </p>
          ) : null}
          {rowError ? (
            <p className="mt-2 rounded-md border border-red-200 bg-red-50 px-2 py-1 text-xs leading-5 text-red-800 md:hidden">
              {rowError}
            </p>
          ) : null}
        </div>
      </td>
      <td className="hidden px-3 py-4 sm:table-cell sm:px-4">
        <StatusBadge status={changeset.status} />
      </td>
      <td className="hidden px-4 py-4 text-slate-700 dark:text-zinc-300 md:table-cell">
        {changeset.author ? <AuthorLabel name={changeset.author} /> : "not returned"}
      </td>
      <td className="hidden px-4 py-4 text-slate-700 dark:text-zinc-300 md:table-cell">
        {changeset.submitRequirements?.requiredApprovals ?? "not returned"}
      </td>
      <td className="hidden whitespace-nowrap px-4 py-4 text-xs text-slate-600 dark:text-zinc-400 lg:table-cell">
        {requiredChecks.length > 0 ? (
          <span
            className="rounded-full bg-slate-100 px-2 py-0.5 font-medium text-slate-700 dark:bg-zinc-800 dark:text-zinc-300"
            title={requiredChecks.join(", ")}
          >
            {requiredChecks.length} required
          </span>
        ) : (
          <span className="text-slate-400 dark:text-zinc-500">None</span>
        )}
      </td>
      <td className="hidden max-w-sm px-4 py-4 text-slate-700 dark:text-zinc-300 md:table-cell">
        {changeset.submitBlockedReason ? (
          <span>{displaySubmitBlockedReason(changeset.submitBlockedReason)}</span>
        ) : (
          <span className="text-slate-400 dark:text-zinc-500">None</span>
        )}
        {rowError ? (
          <p className="mt-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
            {rowError}
          </p>
        ) : null}
      </td>
      {canManage ? (
        <td className="px-3 py-4 sm:px-4">
          <div className="flex flex-wrap justify-end gap-2">
            {mergeable ? (
              <button
                className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 hover:text-zinc-950 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
                disabled={busy || !changesetId}
                onClick={() => mergeMutation.mutate()}
                type="button"
              >
                {mergeMutation.isPending ? "Merging..." : "Merge"}
              </button>
            ) : null}
          </div>
        </td>
      ) : null}
    </tr>
  );
}

function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "Changeset";
}

// Submit (merge) is only valid while the changeset is still open. Once it has
// been submitted/published/abandoned it can't be merged again.
function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}
