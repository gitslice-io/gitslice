import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { SubmitStackEntryResult } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import {
  SliceLoadingBlock,
  SliceNotice
} from "../components/slices/SlicePageParts";
import { shortChangesetId } from "../lib/objectId";
import {
  affectedSubtreeEntries,
  changedPathCount,
  currentPatchsetNumber,
  displaySubmitBlockedReason,
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

export function StackSubmitPage() {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as StackParams;
  const stackId = params.id ?? "";
  const [subtreeRoot, setSubtreeRoot] = useState("");

  const stackQuery = useQuery({
    enabled: Boolean(stackId),
    queryKey: ["stack", stackId],
    queryFn: () => api.getStack({ stackId }),
    refetchInterval: (query) =>
      hasPendingPublish(query.state.data?.entries?.map((entry) => entry.changeset?.status)) ? 2500 : false
  });

  const stack = stackQuery.data;
  const entries = useMemo(() => sortedStackEntries(stack), [stack]);
  const submitRoot = subtreeRoot || stack?.rootEntryId || entries[0]?.changesetId || "";
  const previewEntries = affectedSubtreeEntries({
    entries,
    includeSiblings: false,
    startChangesetId: submitRoot
  });

  const submitMutation = useMutation({
    mutationFn: () =>
      api.submitStack({
        stackId,
        subtreeRootChangesetId: subtreeRoot
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["stack", stackId] });
    }
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    submitMutation.mutate();
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
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[
              { label: "Dependencies", to: "/dependencies" },
              {
                label: shortStackId(stack.id) || stackDisplayName(stack),
                params: { id: stackId },
                to: "/dependencies/$id"
              },
              { label: "Submit" }
            ]}
          />
        }
        primaryAction={
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
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 sm:text-lg">
            {stackDisplayName(stack)}
          </h1>
        }
      />
      <p className="mb-4 text-sm leading-6 text-slate-600">
        {`Submit base-before-dependent against ${stack.targetRef || "the target ref"} from base ${formatCommit(stack.baseCommitId)}.`}
      </p>

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <div className="min-w-0 space-y-6">
          <SubmitPreview entries={previewEntries} />
          <SubmitResultPanel
            entries={entries}
            results={submitMutation.data?.results ?? []}
            stackId={stackId}
          />
          {submitMutation.isError ? (
            <SliceNotice title="Submit failed" tone="error">
              {getErrorMessage(submitMutation.error)}
            </SliceNotice>
          ) : null}
        </div>

        <form
          className="grid content-start gap-4 rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
          onSubmit={submit}
        >
          <h2 className="text-sm font-semibold text-zinc-950">Submit options</h2>
          <label className="grid gap-2 text-sm font-medium text-zinc-800">
            Submit subtree
            <select
              className={inputClass}
              onChange={(event) => setSubtreeRoot(event.target.value)}
              value={subtreeRoot}
            >
              <option value="">All dependencies</option>
              {entries.map((entry) => (
                <option key={entry.changesetId} value={entry.changesetId}>
                  {entryLabel(entry)}
                </option>
              ))}
            </select>
          </label>
          <button
            className={primaryButtonClass}
            disabled={!submitRoot || submitMutation.isPending}
            type="submit"
          >
            {submitMutation.isPending ? "Submitting..." : "Submit dependencies"}
          </button>
        </form>
      </div>
    </section>
  );
}

function SubmitPreview({
  entries
}: {
  entries: ReturnType<typeof affectedSubtreeEntries>;
}) {
  if (!entries.length) {
    return (
      <SliceNotice title="No submit changesets">
        This dependency tree has no changesets in the selected submit set.
      </SliceNotice>
    );
  }

  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-4 py-3">
        <h2 className="text-sm font-semibold text-zinc-950">Submit order</h2>
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
              <p className="mt-1 text-xs text-slate-500">
                patchset {currentPatchsetNumber(entry.changeset) || "none"} - {changedPathCount(entry.changeset)} paths
              </p>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function SubmitResultPanel({
  entries,
  results,
  stackId
}: {
  entries: ReturnType<typeof sortedStackEntries>;
  results: SubmitStackEntryResult[];
  stackId: string;
}) {
  if (!results.length) {
    return (
      <SliceNotice title="No submit result yet">
        Run submit to see accepted, pending, submitted, and blocked changesets.
      </SliceNotice>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-4 py-3">
        <h2 className="text-sm font-semibold text-zinc-950">Submit result</h2>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-4 py-3">Changeset</th>
              <th className="px-4 py-3">Status</th>
              <th className="hidden px-4 py-3 md:table-cell">Commit</th>
              <th className="hidden px-4 py-3 lg:table-cell">Blocked reason</th>
              <th className="px-4 py-3 text-right">Open</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200">
            {results.map((result) => {
              const entry = entryByChangesetId(entries, result.changesetId || "");
              const detailId =
                shortChangesetId(result.changesetId || "") ||
                result.changesetId ||
                "";

              return (
                <tr key={result.changesetId}>
                  <td className="px-4 py-4">
                    <span className="font-semibold text-zinc-950">
                      {entry ? entryLabel(entry) : detailId || "changeset"}
                    </span>
                    {entry ? (
                      <p className="mt-1 break-words text-sm text-slate-700">
                        {entryTitle(entry)}
                      </p>
                    ) : null}
                  </td>
                  <td className="px-4 py-4">
                    <StackStatusBadge status={result.status} />
                  </td>
                  <td
                    className="hidden px-4 py-4 font-mono text-xs text-slate-600 md:table-cell"
                    title={result.commitId}
                  >
                    {formatCommit(result.commitId)}
                  </td>
                  <td className="hidden max-w-sm px-4 py-4 text-slate-700 lg:table-cell">
                    {displaySubmitBlockedReason(result.blockedReason) || "None"}
                  </td>
                  <td className="px-4 py-4 text-right">
                    <Link
                      className={secondaryButtonClass}
                      params={{ id: detailId }}
                      search={{ dependency: stackId } as never}
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
    </div>
  );
}

function hasPendingPublish(statuses: Array<string | undefined> | undefined) {
  return Boolean(statuses?.some((status) => status === "pending_publish"));
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
