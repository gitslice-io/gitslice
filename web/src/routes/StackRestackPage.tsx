import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { Changeset, PatchsetConflict } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader
} from "../components/slices/SlicePageParts";
import { shortChangesetId } from "../lib/objectId";
import {
  affectedSubtreeEntries,
  changedPathCount,
  conflictCount,
  currentPatchset,
  currentPatchsetNumber,
  entryByChangesetId,
  entryDepth,
  entryLabel,
  entryTitle,
  formatCommit,
  getErrorMessage,
  primaryButtonClass,
  secondaryButtonClass,
  shortStackId,
  sortedStackEntries,
  stackDisplayName,
  StackStatusBadge
} from "./stackPageUtils";

interface StackParams {
  id?: string;
}

export function StackRestackPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as StackParams;
  const stackId = params.id ?? "";
  const [startEntryId, setStartEntryId] = useState("");
  const [includeSiblings, setIncludeSiblings] = useState(false);
  const [targetBaseCommitId, setTargetBaseCommitId] = useState("");

  const stackQuery = useQuery({
    enabled: Boolean(stackId),
    queryKey: ["stack", stackId],
    queryFn: () => api.getStack({ stackId })
  });

  const stack = stackQuery.data;
  const entries = useMemo(() => sortedStackEntries(stack), [stack]);
  const defaultStartId =
    startEntryId || stack?.activeEntryId || stack?.rootEntryId || entries[0]?.changesetId || "";
  const previewEntries = affectedSubtreeEntries({
    entries,
    includeSiblings,
    startChangesetId: defaultStartId
  });

  const restackMutation = useMutation({
    mutationFn: () =>
      api.restack({
        includeSiblings,
        stackId,
        startChangesetId: defaultStartId,
        targetBaseCommitId: targetBaseCommitId.trim()
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    restackMutation.mutate();
  };

  if (stackQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (stackQuery.isError) {
    return (
      <ActionMessage
        message={getErrorMessage(stackQuery.error)}
        title="Unable to load dependencies"
      />
    );
  }

  if (!stack) {
    return <ActionMessage message="The API returned no dependency tree." title="Dependency tree not found" />;
  }

  return (
    <section className="mx-auto w-full max-w-[84rem]">
      <div className="mb-4">
        <Breadcrumb
          items={[
            { label: "Dependencies", to: "/dependencies" },
            {
              label: shortStackId(stack.id) || stackDisplayName(stack),
              params: { id: stackId },
              to: "/dependencies/$id"
            },
            { label: "Update" }
          ]}
        />
      </div>

      <SlicePageHeader
        actions={
          <button
            className={secondaryButtonClass}
            onClick={() => {
              void navigate({ params: { id: stackId }, to: "/dependencies/$id" });
            }}
            type="button"
          >
            Back to dependencies
          </button>
        }
        description="Preview the dependent changesets that will be replayed, then create updated patchsets through the server."
        eyebrow="Update dependents"
        title={stackDisplayName(stack)}
      />

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <div className="min-w-0 space-y-6">
          <RestackPreview entries={previewEntries} />
          <RestackResult entries={restackMutation.data?.entries ?? []} />
          {restackMutation.isError ? (
            <SliceNotice title="Update failed" tone="error">
              {getErrorMessage(restackMutation.error)}
            </SliceNotice>
          ) : null}
        </div>

        <form
          className="grid content-start gap-4 rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
          onSubmit={submit}
        >
          <h2 className="text-sm font-semibold text-zinc-950">Update options</h2>
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Update from
            <select
              className={inputClass}
              onChange={(event) => setStartEntryId(event.target.value)}
              value={defaultStartId}
            >
              {entries.map((entry) => (
                <option key={entry.changesetId} value={entry.changesetId}>
                  {entryLabel(entry)}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-start gap-2 text-sm font-medium text-zinc-800">
            <input
              checked={includeSiblings}
              className="mt-1 h-4 w-4 rounded border-slate-300 text-zinc-950 focus:ring-zinc-300"
              onChange={(event) => setIncludeSiblings(event.target.checked)}
              type="checkbox"
            />
            Include sibling subtrees
          </label>
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Target base commit
            <input
              className={inputClass}
              onChange={(event) => setTargetBaseCommitId(event.target.value)}
              placeholder={formatCommit(stack.baseCommitId)}
              value={targetBaseCommitId}
            />
          </label>
          <button
            className={primaryButtonClass}
            disabled={!defaultStartId || restackMutation.isPending}
            type="submit"
          >
            {restackMutation.isPending ? "Updating..." : "Update dependents"}
          </button>
        </form>
      </div>
    </section>
  );
}

function RestackPreview({
  entries
}: {
  entries: ReturnType<typeof affectedSubtreeEntries>;
}) {
  if (!entries.length) {
    return (
      <SliceNotice title="No affected changesets">
        This dependency tree has no changeset that can be updated from the selected point.
      </SliceNotice>
    );
  }

  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-4 py-3">
        <h2 className="text-sm font-semibold text-zinc-950">Affected changesets</h2>
      </div>
      <div className="divide-y divide-slate-200">
        {entries.map((entry) => (
          <div className="px-4 py-3" key={entry.changesetId}>
            <div style={{ paddingLeft: `${Math.min(entryDepth(entry), 8) * 1.25}rem` }}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-semibold text-zinc-950">{entryLabel(entry)}</span>
                <StackStatusBadge status={entry.state || entry.changeset?.status} />
              </div>
              <p className="mt-1 break-words text-sm text-slate-700">
                {entryTitle(entry)}
              </p>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function RestackResult({ entries }: { entries: Changeset[] }) {
  if (!entries.length) {
    return (
      <SliceNotice title="No update result yet">
        Run update to create new patchsets or confirm that selected changesets are unchanged.
      </SliceNotice>
    );
  }

  const conflictRows = entries.flatMap((changeset) =>
    (currentPatchset(changeset)?.conflicts ?? []).map((conflict) => ({
      changeset,
      conflict
    }))
  );

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-4 py-3">
        <h2 className="text-sm font-semibold text-zinc-950">Update result</h2>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-4 py-3">Changeset</th>
              <th className="px-4 py-3">Result</th>
              <th className="hidden px-4 py-3 md:table-cell">Patchset</th>
              <th className="hidden px-4 py-3 lg:table-cell">Paths</th>
              <th className="px-4 py-3 text-right">Open</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            {entries.map((changeset) => {
              const conflicts = conflictCount(changeset);
              const displayStatus = conflicts > 0 ? "conflict" : changeset.status || "clean";
              const detailId = shortChangesetId(changeset.id || "") || changeset.id || "";

              return (
                <tr key={changeset.id}>
                  <td className="px-4 py-4">
                    <span className="font-semibold text-zinc-950">
                      {changeset.handle || detailId || "changeset"}
                    </span>
                    <p className="mt-1 break-words text-sm text-slate-700">
                      {changeset.title || "Untitled changeset"}
                    </p>
                  </td>
                  <td className="px-4 py-4">
                    <StackStatusBadge status={displayStatus} />
                  </td>
                  <td className="hidden px-4 py-4 text-slate-700 md:table-cell">
                    {currentPatchsetNumber(changeset) || "none"}
                  </td>
                  <td className="hidden px-4 py-4 text-slate-700 lg:table-cell">
                    {changedPathCount(changeset)}
                  </td>
                  <td className="px-4 py-4 text-right">
                    <Link
                      className={secondaryButtonClass}
                      params={{ id: detailId }}
                      to="/cs/$id"
                    >
                      Detail
                    </Link>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {conflictRows.length ? (
        <div className="border-t border-slate-200 bg-rose-50/50 px-4 py-4">
          <h3 className="text-sm font-semibold text-rose-950">
            Conflict details
          </h3>
          <div className="mt-3 grid gap-3">
            {conflictRows.map(({ changeset, conflict }, index) => (
              <RestackConflictDetail
                changeset={changeset}
                conflict={conflict}
                key={`${changeset.id || "changeset"}-${conflict.path || index}`}
              />
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function RestackConflictDetail({
  changeset,
  conflict
}: {
  changeset: Changeset;
  conflict: PatchsetConflict;
}) {
  const detailId = shortChangesetId(changeset.id || "") || changeset.id || "";

  return (
    <div className="rounded-md border border-rose-200 bg-white p-3">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="break-all font-mono text-sm font-semibold text-zinc-950">
            {conflict.path || "path not returned"}
          </div>
          <div className="mt-1 flex flex-wrap gap-2 text-xs text-slate-600">
            <span className="rounded border border-rose-200 bg-rose-50 px-2 py-1 font-semibold text-rose-800">
              {conflict.conflictClass === "restack"
                ? "base update"
                : conflict.conflictClass || "conflict"}
            </span>
            <span>{changeset.handle || detailId || "changeset"}</span>
          </div>
        </div>
        {detailId ? (
          <Link
            className="text-sm font-semibold text-rose-900 underline decoration-rose-300 underline-offset-4 hover:decoration-rose-800"
            params={{ id: detailId }}
            to="/cs/$id"
          >
            Open changeset
          </Link>
        ) : null}
      </div>
      <dl className="mt-3 grid gap-2 text-xs text-slate-700 sm:grid-cols-2">
        <ConflictMetadata label="Old base" value={conflict.oldBaseCommitId} />
        <ConflictMetadata label="New base" value={conflict.newBaseCommitId} />
        <ConflictMetadata label="Base fingerprint" value={conflict.baseFingerprint} />
        <ConflictMetadata label="Remote fingerprint" value={conflict.remoteFingerprint} />
      </dl>
    </div>
  );
}

function ConflictMetadata({
  label,
  value
}: {
  label: string;
  value?: string;
}) {
  return (
    <div className="min-w-0">
      <dt className="font-semibold text-slate-500">{label}</dt>
      <dd className="break-all font-mono text-zinc-900">{value || "not returned"}</dd>
    </div>
  );
}

function ActionMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <SliceNotice title={title} tone="error">
        {message}
      </SliceNotice>
    </section>
  );
}

const inputClass =
  "h-11 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200";
