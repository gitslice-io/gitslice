import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";

import type {
  Changeset,
  FileEdit,
  Patchset,
  PathBase,
  PathCoverage,
  PathSetEntry,
  SubmitRequirements
} from "../api/types";
import { useApi } from "../api/useApi";
import {
  FileEditForm,
  clientPreview,
  createEmptyEditDraft,
  prepareFileEdits,
  type FileEditDraft
} from "../components/changesets/FileEditForm";
import { cn } from "../lib/cn";

export function ChangesetDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as { id?: string };
  const search = useSearch({ strict: false }) as { patchset?: string | number };
  const changesetId = params.id ?? "";
  const selectedPatchset = search.patchset ? String(search.patchset) : "";
  const [abandonReason, setAbandonReason] = useState("");
  const [showAddPatchset, setShowAddPatchset] = useState(false);
  const [patchsetRows, setPatchsetRows] = useState<FileEditDraft[]>([
    createEmptyEditDraft()
  ]);

  const changesetQuery = useQuery({
    enabled: Boolean(changesetId),
    queryKey: ["changeset", changesetId],
    queryFn: () => api.getChangeset({ changesetId }),
    refetchInterval: (query) =>
      query.state.data?.status === "pending_publish" ? 2500 : false
  });

  const changeset = changesetQuery.data;
  const canonicalChangesetId = changeset?.id || changesetId;
  const patchsetPreview = useMemo(
    () => clientPreview(patchsetRows),
    [patchsetRows]
  );

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["changeset", changesetId]
    });
  };

  const submitMutation = useMutation({
    mutationFn: async () => {
      if (!changeset?.currentPatchsetId) {
        throw new Error("This changeset has no current patchset to submit.");
      }

      return api.submitChangeset({
        changesetId: canonicalChangesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onSuccess: invalidateChangeset
  });

  const abandonMutation = useMutation({
    mutationFn: async () =>
      api.abandonChangeset({
        changesetId: canonicalChangesetId,
        reason: abandonReason.trim()
      }),
    onSuccess: invalidateChangeset
  });

  const addPatchsetMutation = useMutation({
    mutationFn: async () => {
      if (!changeset?.authoringSlice) {
        throw new Error("Changeset did not return an authoring slice.");
      }
      if (!changeset.currentPatchsetId) {
        throw new Error("This changeset has no current patchset.");
      }

      const fileEdits = await prepareFileEdits({
        rows: patchsetRows,
        slice: changeset.authoringSlice,
        uploadBlob: api.uploadBlob
      });

      return api.updateChangeset({
        changesetId: canonicalChangesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId,
        baseCommitId: changeset.baseCommitId,
        fileEdits
      });
    },
    onSuccess: async () => {
      setPatchsetRows([createEmptyEditDraft()]);
      setShowAddPatchset(false);
      await invalidateChangeset();
    }
  });

  const focusPatchset = (number?: string) => {
    void navigate({
      to: "/changesets/$id",
      params: { id: changesetId },
      search: (previous) => {
        const next = { ...previous } as Record<string, unknown>;
        if (number) {
          next.patchset = number;
        } else {
          delete next.patchset;
        }
        return next;
      }
    });
  };

  const submitAbandon = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    abandonMutation.mutate();
  };

  const submitPatchset = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    addPatchsetMutation.mutate();
  };

  if (!changesetId) {
    return <PageMessage title="Missing changeset" message="No changeset id was provided." />;
  }

  if (changesetQuery.isLoading) {
    return <ChangesetSkeleton />;
  }

  if (changesetQuery.isError) {
    return (
      <PageMessage
        title="Unable to load changeset"
        message={errorMessage(changesetQuery.error)}
      />
    );
  }

  if (!changeset) {
    return <PageMessage title="Changeset not found" message="The API returned no changeset." />;
  }

  const patchsets = changeset.patchsets ?? [];
  const busy =
    submitMutation.isPending ||
    abandonMutation.isPending ||
    addPatchsetMutation.isPending;

  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="rounded-lg border border-slate-200 bg-white">
        <div className="border-b border-slate-200 px-5 py-5">
          <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div className="min-w-0">
              <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
                {changeset.handle || changesetLabel(changeset)}
              </p>
              <h1 className="mt-2 text-2xl font-semibold tracking-normal text-zinc-950">
                {changeset.title || "Untitled changeset"}
              </h1>
              {changeset.description ? (
                <p className="mt-2 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-slate-600">
                  {changeset.description}
                </p>
              ) : null}
            </div>
            <span className={cn("w-fit rounded-full px-3 py-1 text-xs font-semibold", statusClass(changeset.status))}>
              {changeset.status || "unknown"}
            </span>
          </div>
        </div>

        <div className="grid gap-4 px-5 py-5 md:grid-cols-2 xl:grid-cols-4">
          <MetaItem label="Author" value={changeset.author} />
          <MetaItem label="Authoring slice" value={formatSliceRef(changeset.authoringSlice)} />
          <MetaItem label="Target ref" value={changeset.targetRef} />
          <MetaItem label="Base commit" value={changeset.baseCommitId} mono />
          <MetaItem label="Current patchset" value={changeset.currentPatchsetNumber} />
          <MetaItem label="Commit id" value={changeset.commitId} mono />
          <MetaItem label="Pending publish id" value={changeset.pendingPublishId} mono />
          <MetaItem label="Submit blocked" value={changeset.submitBlockedReason} />
        </div>

        <details className="border-t border-slate-200 px-5 py-4">
          <summary className="cursor-pointer text-sm font-medium text-slate-700">
            Debug identifiers
          </summary>
          <pre className="mt-3 max-h-72 overflow-auto rounded-md bg-slate-950 p-4 font-mono text-xs leading-5 text-slate-100">
            {JSON.stringify(debugChangeset(changeset), null, 2)}
          </pre>
        </details>
      </div>

      <div className="mt-6 rounded-lg border border-slate-200 bg-white p-5">
        <div className="flex flex-col gap-4 xl:flex-row xl:items-end xl:justify-between">
          <div>
            <h2 className="text-base font-semibold tracking-normal text-zinc-950">
              Actions
            </h2>
            <p className="mt-1 text-sm text-slate-600">
              Submit uses the returned current patchset id. Add Patchset uploads
              pasted content with the same edit controls as the create page.
            </p>
          </div>
          <div className="flex flex-wrap gap-3">
            <button
              className="rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-medium text-zinc-800 transition hover:border-zinc-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
              disabled={busy}
              onClick={() => setShowAddPatchset((value) => !value)}
              type="button"
            >
              {showAddPatchset ? "Cancel Patchset" : "Add Patchset"}
            </button>
            <button
              className="rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
              disabled={busy || !changeset.currentPatchsetId}
              onClick={() => submitMutation.mutate()}
              type="button"
            >
              {submitMutation.isPending ? "Submitting..." : "Submit"}
            </button>
          </div>
        </div>

        {submitMutation.isSuccess ? (
          <div className="mt-4 rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-900">
            Submit returned status {submitMutation.data.status || "unknown"}
            {submitMutation.data.pendingPublishId
              ? ` with publish id ${submitMutation.data.pendingPublishId}`
              : ""}
            .
          </div>
        ) : null}
        {submitMutation.isError ? (
          <ErrorBox className="mt-4" error={submitMutation.error} />
        ) : null}

        <form className="mt-5 grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto]" onSubmit={submitAbandon}>
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Abandon reason
            <input
              className="h-10 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
              disabled={busy}
              onChange={(event) => setAbandonReason(event.target.value)}
              placeholder="Optional reason"
              value={abandonReason}
            />
          </label>
          <button
            className="self-end rounded-md border border-red-300 bg-white px-4 py-2.5 text-sm font-medium text-red-700 transition hover:border-red-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
            disabled={busy}
            type="submit"
          >
            {abandonMutation.isPending ? "Abandoning..." : "Abandon"}
          </button>
        </form>
        {abandonMutation.isError ? (
          <ErrorBox className="mt-4" error={abandonMutation.error} />
        ) : null}

        {showAddPatchset ? (
          <form className="mt-6 grid gap-4" onSubmit={submitPatchset}>
            <FileEditForm
              disabled={busy}
              onRowsChange={setPatchsetRows}
              rows={patchsetRows}
              title="Add Patchset"
            />
            {patchsetPreview ? (
              <pre className="max-h-64 overflow-auto rounded-lg border border-slate-200 bg-slate-50 p-4 font-mono text-sm leading-6 text-slate-800">
                {patchsetPreview}
              </pre>
            ) : null}
            {addPatchsetMutation.isError ? <ErrorBox error={addPatchsetMutation.error} /> : null}
            <div>
              <button
                className="rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60"
                disabled={busy}
                type="submit"
              >
                {addPatchsetMutation.isPending ? "Adding Patchset..." : "Add Patchset"}
              </button>
            </div>
          </form>
        ) : null}
      </div>

      <section className="mt-8">
        <div className="flex flex-col gap-4 border-b border-slate-200 pb-4 lg:flex-row lg:items-end lg:justify-between">
          <div>
            <h2 className="text-lg font-semibold tracking-normal text-zinc-950">
              Patchsets
            </h2>
            <p className="mt-1 text-sm text-slate-600">
              Returned metadata only: changed paths, file edits, coverage, path
              bases, path sets, conflicts, and submit requirement ids.
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <button
              className={cn(tabClass(!selectedPatchset))}
              onClick={() => focusPatchset()}
              type="button"
            >
              All
            </button>
            {patchsets.map((patchset) => (
              <button
                className={cn(tabClass(selectedPatchset === String(patchset.number)))}
                key={patchset.id || patchset.number}
                onClick={() => focusPatchset(String(patchset.number ?? ""))}
                type="button"
              >
                PS{patchset.number ?? "?"}
              </button>
            ))}
          </div>
        </div>

        {patchsets.length === 0 ? (
          <div className="mt-5 rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-600">
            No patchsets returned.
          </div>
        ) : (
          <div className="mt-5 grid gap-5">
            {patchsets.map((patchset) => (
              <PatchsetCard
                focused={selectedPatchset === String(patchset.number)}
                key={patchset.id || patchset.number}
                patchset={patchset}
              />
            ))}
          </div>
        )}
      </section>
    </section>
  );
}

function PatchsetCard({
  focused,
  patchset
}: {
  focused: boolean;
  patchset: Patchset;
}) {
  return (
    <article
      className={cn(
        "rounded-lg border bg-white",
        focused ? "border-zinc-950" : "border-slate-200"
      )}
    >
      <div className="border-b border-slate-200 px-5 py-4">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-base font-semibold tracking-normal text-zinc-950">
                {patchset.handle || `PS${patchset.number ?? "?"}`}
              </h3>
              {focused ? (
                <span className="rounded-full bg-zinc-950 px-2.5 py-1 text-xs font-semibold text-white">
                  Focused
                </span>
              ) : null}
              {patchset.kind ? (
                <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-medium text-slate-700">
                  {patchset.kind}
                </span>
              ) : null}
            </div>
            <p className="mt-1 text-sm text-slate-600">
              author {patchset.author || "not returned"} | base{" "}
              <span className="font-mono">{patchset.baseCommitId || "not returned"}</span>
              {patchset.createdAt ? ` | ${formatDate(patchset.createdAt)}` : ""}
            </p>
          </div>
          <details className="text-sm">
            <summary className="cursor-pointer font-medium text-slate-700">
              Patchset ids
            </summary>
            <pre className="mt-2 max-w-full overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs leading-5 text-slate-100">
              {JSON.stringify(
                { id: patchset.id, changesetId: patchset.changesetId },
                null,
                2
              )}
            </pre>
          </details>
        </div>
      </div>

      <div className="grid gap-6 px-5 py-5">
        <ListBlock title="Changed paths" values={patchset.changedPaths} mono />
        <FileEditTable edits={patchset.fileEdits ?? []} />
        <CoverageTable coverage={patchset.coverage ?? []} />
        <PathBaseTable bases={patchset.pathBases ?? []} />
        <PathSetSummary
          readSet={patchset.readSet ?? []}
          writeSet={patchset.writeSet ?? []}
        />
        <SubmitRequirementsBlock requirements={patchset.submitRequirements} />
        <ListBlock
          title="Conflicts"
          values={(patchset.conflicts ?? []).map(
            (conflict) =>
              `${conflict.path || "unknown"} ${conflict.conflictClass || "conflict"}`
          )}
        />
      </div>
    </article>
  );
}

function FileEditTable({ edits }: { edits: FileEdit[] }) {
  if (edits.length === 0) {
    return <EmptyBlock title="File edits" message="No file edits returned." />;
  }

  return (
    <DataBlock title="File edits">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="py-2 pr-4">Op</th>
              <th className="px-4 py-2">Path</th>
              <th className="px-4 py-2">Old path</th>
              <th className="px-4 py-2">Blob</th>
              <th className="px-4 py-2">Content hash</th>
              <th className="py-2 pl-4">Mode</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 text-slate-700">
            {edits.map((edit, index) => (
              <tr key={`${edit.path}-${edit.oldPath}-${index}`}>
                <td className="py-2 pr-4 font-medium text-zinc-950">
                  {edit.op || "not returned"}
                </td>
                <td className="px-4 py-2 font-mono">{edit.path || "not returned"}</td>
                <td className="px-4 py-2 font-mono">{edit.oldPath || "none"}</td>
                <td className="px-4 py-2 font-mono">{edit.blobId || "none"}</td>
                <td className="px-4 py-2 font-mono">{edit.contentHash || "none"}</td>
                <td className="py-2 pl-4 font-mono">{edit.mode ?? "none"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </DataBlock>
  );
}

function CoverageTable({ coverage }: { coverage: PathCoverage[] }) {
  if (coverage.length === 0) {
    return <EmptyBlock title="Coverage" message="No coverage returned." />;
  }

  return (
    <DataBlock title="Coverage">
      <div className="grid gap-2">
        {coverage.map((item, index) => (
          <div
            className="grid gap-2 rounded-md bg-slate-50 px-3 py-2 text-sm md:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)]"
            key={`${item.path}-${index}`}
          >
            <span className="font-mono text-slate-800">{item.path || "not returned"}</span>
            <span className="font-mono text-slate-600">
              {formatArray(item.coveringSliceIds)}
            </span>
          </div>
        ))}
      </div>
    </DataBlock>
  );
}

function PathBaseTable({ bases }: { bases: PathBase[] }) {
  if (bases.length === 0) {
    return <EmptyBlock title="Path bases" message="No path bases returned." />;
  }

  return (
    <DataBlock title="Path bases">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="py-2 pr-4">Path</th>
              <th className="px-4 py-2">Base commit</th>
              <th className="px-4 py-2">Exists</th>
              <th className="px-4 py-2">Kind</th>
              <th className="px-4 py-2">Blob</th>
              <th className="px-4 py-2">Content hash</th>
              <th className="px-4 py-2">Tree</th>
              <th className="py-2 pl-4">Check</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 text-slate-700">
            {bases.map((base, index) => (
              <tr key={`${base.path}-${index}`}>
                <td className="py-2 pr-4 font-mono">{base.path || "not returned"}</td>
                <td className="px-4 py-2 font-mono">{base.baseCommitId || "none"}</td>
                <td className="px-4 py-2">{base.exists ? "yes" : "no"}</td>
                <td className="px-4 py-2">{base.entryKind || "none"}</td>
                <td className="px-4 py-2 font-mono">{base.blobId || "none"}</td>
                <td className="px-4 py-2 font-mono">{base.contentHash || "none"}</td>
                <td className="px-4 py-2 font-mono">{base.treeId || "none"}</td>
                <td className="py-2 pl-4">
                  <span className="font-mono">{base.check || "none"}</span>
                  {base.entryFingerprint ? (
                    <span className="mt-1 block font-mono text-xs text-slate-500">
                      {base.entryFingerprint}
                    </span>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </DataBlock>
  );
}

function PathSetSummary({
  readSet,
  writeSet
}: {
  readSet: PathSetEntry[];
  writeSet: PathSetEntry[];
}) {
  return (
    <DataBlock title="Read and write sets">
      <div className="grid gap-4 md:grid-cols-2">
        <PathSetList title="Read set" values={readSet} />
        <PathSetList title="Write set" values={writeSet} />
      </div>
    </DataBlock>
  );
}

function PathSetList({
  title,
  values
}: {
  title: string;
  values: PathSetEntry[];
}) {
  return (
    <div>
      <h4 className="text-sm font-medium text-zinc-950">{title}</h4>
      {values.length === 0 ? (
        <p className="mt-2 text-sm text-slate-600">None returned.</p>
      ) : (
        <div className="mt-2 flex flex-wrap gap-2">
          {values.map((entry, index) => (
            <span
              className="rounded-md bg-slate-100 px-2.5 py-1 font-mono text-xs text-slate-700"
              key={`${entry.path}-${index}`}
            >
              {entry.path || "not returned"}
              {entry.recursive ? " recursive" : ""}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function SubmitRequirementsBlock({
  requirements
}: {
  requirements?: SubmitRequirements;
}) {
  return (
    <DataBlock title="Submit requirements">
      <div className="grid gap-3 text-sm text-slate-700 md:grid-cols-2">
        <RequirementItem
          label="Required approvals"
          value={requirements?.requiredApprovals ?? "none returned"}
        />
        <RequirementItem
          label="Required checks"
          value={formatArray(requirements?.requiredChecks)}
        />
        <RequirementItem
          label="Path lock ids"
          value={formatArray(requirements?.pathLockIds)}
        />
        <RequirementItem
          label="Source slice definition hash"
          value={requirements?.sourceSliceDefinitionHash || "none returned"}
          mono
        />
        <RequirementItem
          label="Source path lock set hash"
          value={requirements?.sourcePathLockSetHash || "none returned"}
          mono
        />
      </div>
    </DataBlock>
  );
}

function RequirementItem({
  label,
  mono = false,
  value
}: {
  label: string;
  mono?: boolean;
  value: string | number;
}) {
  return (
    <div className="rounded-md bg-slate-50 px-3 py-2">
      <dt className="text-xs font-semibold uppercase tracking-normal text-slate-500">
        {label}
      </dt>
      <dd className={cn("mt-1 break-words text-slate-800", mono && "font-mono text-xs")}>
        {value}
      </dd>
    </div>
  );
}

function ListBlock({
  mono = false,
  title,
  values
}: {
  mono?: boolean;
  title: string;
  values?: string[];
}) {
  if (!values || values.length === 0) {
    return <EmptyBlock title={title} message={`No ${title.toLowerCase()} returned.`} />;
  }

  return (
    <DataBlock title={title}>
      <div className="flex flex-wrap gap-2">
        {values.map((value) => (
          <span
            className={cn(
              "rounded-md bg-slate-100 px-2.5 py-1 text-sm text-slate-700",
              mono && "font-mono text-xs"
            )}
            key={value}
          >
            {value}
          </span>
        ))}
      </div>
    </DataBlock>
  );
}

function DataBlock({
  children,
  title
}: {
  children: ReactNode;
  title: string;
}) {
  return (
    <section>
      <h4 className="text-sm font-semibold tracking-normal text-zinc-950">
        {title}
      </h4>
      <div className="mt-3">{children}</div>
    </section>
  );
}

function EmptyBlock({ message, title }: { message: string; title: string }) {
  return (
    <DataBlock title={title}>
      <div className="rounded-md border border-dashed border-slate-300 bg-slate-50 px-3 py-3 text-sm text-slate-600">
        {message}
      </div>
    </DataBlock>
  );
}

function MetaItem({
  label,
  mono = false,
  value
}: {
  label: string;
  mono?: boolean;
  value?: string | number;
}) {
  return (
    <div>
      <dt className="text-xs font-semibold uppercase tracking-normal text-slate-500">
        {label}
      </dt>
      <dd className={cn("mt-1 break-words text-sm text-zinc-950", mono && "font-mono text-xs")}>
        {value === undefined || value === "" ? "not returned" : value}
      </dd>
    </div>
  );
}

function ErrorBox({ className, error }: { className?: string; error: unknown }) {
  return (
    <div className={cn("rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800", className)}>
      {errorMessage(error)}
    </div>
  );
}

function PageMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="rounded-lg border border-slate-200 bg-white p-6">
        <h1 className="text-xl font-semibold tracking-normal text-zinc-950">
          {title}
        </h1>
        <p className="mt-2 text-sm text-slate-600">{message}</p>
      </div>
    </section>
  );
}

function ChangesetSkeleton() {
  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="rounded-lg border border-slate-200 bg-white p-5">
        <div className="h-4 w-40 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 h-8 w-2/3 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 grid gap-4 md:grid-cols-4">
          {Array.from({ length: 8 }).map((_, index) => (
            <div className="h-12 animate-pulse rounded bg-slate-100" key={index} />
          ))}
        </div>
      </div>
    </section>
  );
}

function changesetLabel(changeset: Changeset) {
  if (changeset.number) {
    return `Changeset ${changeset.number}`;
  }
  return "Changeset";
}

function debugChangeset(changeset: Changeset) {
  return {
    id: changeset.id,
    currentPatchsetId: changeset.currentPatchsetId,
    patchsets: (changeset.patchsets ?? []).map((patchset) => ({
      id: patchset.id,
      changesetId: patchset.changesetId,
      number: patchset.number,
      handle: patchset.handle
    }))
  };
}

function formatSliceRef(ref: Changeset["authoringSlice"]) {
  if (!ref?.account && !ref?.slice) {
    return "not returned";
  }
  return `${ref.account ?? "unknown"}/${ref.slice ?? "unknown"}`;
}

function formatArray(values?: string[]) {
  return values && values.length > 0 ? values.join(", ") : "none returned";
}

function formatDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

function statusClass(status?: string) {
  switch (status) {
    case "draft":
      return "bg-slate-100 text-slate-800";
    case "pending_publish":
      return "bg-amber-100 text-amber-900";
    case "submitted":
      return "bg-emerald-100 text-emerald-900";
    case "abandoned":
      return "bg-red-100 text-red-800";
    default:
      return "bg-slate-100 text-slate-700";
  }
}

function tabClass(active: boolean) {
  return cn(
    "rounded-md px-3 py-2 text-sm font-medium transition active:translate-y-px",
    active
      ? "bg-zinc-950 text-white"
      : "border border-slate-300 bg-white text-slate-700 hover:border-zinc-500"
  );
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}
