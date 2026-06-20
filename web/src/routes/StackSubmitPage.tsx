import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { SubmitStackEntryResult } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { Button, Card, PageHeader, surfaceClassName } from "../components/ui";
import { cn } from "../lib/cn";
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
  nativeControlClassName,
  secondaryButtonClass,
  StackLoadingBlock,
  StackNotice,
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
        <StackLoadingBlock />
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
            { label: "Submit" }
          ]}
        />
      </div>

      <PageHeader
        actions={
          <Button
            onClick={() => {
              void navigate({ params: { id: stackId }, to: "/dependencies/$id" });
            }}
            type="button"
            variant="secondary"
          >
            Back to dependencies
          </Button>
        }
        description={`Submit base-before-dependent against ${stack.targetRef || "the target ref"} from base ${formatCommit(stack.baseCommitId)}.`}
        eyebrow="Submit dependencies"
        title={<span className="font-serif">{stackDisplayName(stack)}</span>}
      />

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <div className="min-w-0 space-y-6">
          <SubmitPreview entries={previewEntries} />
          <SubmitResultPanel
            entries={entries}
            results={submitMutation.data?.results ?? []}
            stackId={stackId}
          />
          {submitMutation.isError ? (
            <StackNotice title="Submit failed" tone="error">
              {getErrorMessage(submitMutation.error)}
            </StackNotice>
          ) : null}
        </div>

        <form
          className={cn(surfaceClassName({ level: "low" }), "grid content-start gap-4 p-5")}
          onSubmit={submit}
        >
          <h2 className="text-sm font-semibold text-on-surface">Submit options</h2>
          <label className="grid gap-2 font-label text-sm font-semibold text-on-surface">
            Submit subtree
            <select
              className={nativeControlClassName}
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
          <Button
            disabled={!submitRoot || submitMutation.isPending}
            type="submit"
          >
            {submitMutation.isPending ? "Submitting..." : "Submit dependencies"}
          </Button>
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
      <StackNotice title="No submit changesets">
        This dependency tree has no changesets in the selected submit set.
      </StackNotice>
    );
  }

  return (
    <Card level="low" padding="none">
      <div className="bg-surface-container-high px-4 py-3">
        <h2 className="text-sm font-semibold text-on-surface">Submit order</h2>
      </div>
      <div>
        {entries.map((entry) => (
          <div
            className="px-4 py-3 odd:bg-surface-container-lowest even:bg-surface-container-low"
            key={entry.changesetId}
          >
            <div style={{ paddingLeft: `${Math.min(entryDepth(entry), 8) * 1.25}rem` }}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-semibold text-on-surface">{entryLabel(entry)}</span>
                <StackStatusBadge status={entry.state || entry.changeset?.status} />
              </div>
              <p className="mt-1 break-words text-sm text-on-surface-variant">
                {entryTitle(entry)}
              </p>
              <p className="mt-1 text-xs text-on-surface-muted">
                patchset {currentPatchsetNumber(entry.changeset) || "none"} - {changedPathCount(entry.changeset)} paths
              </p>
            </div>
          </div>
        ))}
      </div>
    </Card>
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
      <StackNotice title="No submit result yet">
        Run submit to see accepted, pending, submitted, and blocked changesets.
      </StackNotice>
    );
  }

  return (
    <Card level="low" padding="none">
      <div className="bg-surface-container-high px-4 py-3">
        <h2 className="text-sm font-semibold text-on-surface">Submit result</h2>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full text-left text-sm">
          <thead className="bg-surface-container font-label text-xs font-semibold uppercase tracking-normal text-on-surface-variant">
            <tr>
              <th className="px-4 py-3">Changeset</th>
              <th className="px-4 py-3">Status</th>
              <th className="hidden px-4 py-3 md:table-cell">Commit</th>
              <th className="hidden px-4 py-3 lg:table-cell">Blocked reason</th>
              <th className="px-4 py-3 text-right">Open</th>
            </tr>
          </thead>
          <tbody>
            {results.map((result) => {
              const entry = entryByChangesetId(entries, result.changesetId || "");
              const detailId =
                shortChangesetId(result.changesetId || "") ||
                result.changesetId ||
                "";

              return (
                <tr
                  className="align-top odd:bg-surface-container-lowest even:bg-surface-container-low"
                  key={result.changesetId}
                >
                  <td className="px-4 py-4">
                    <span className="font-semibold text-on-surface">
                      {entry ? entryLabel(entry) : detailId || "changeset"}
                    </span>
                    {entry ? (
                      <p className="mt-1 break-words text-sm text-on-surface-variant">
                        {entryTitle(entry)}
                      </p>
                    ) : null}
                  </td>
                  <td className="px-4 py-4">
                    <StackStatusBadge status={result.status} />
                  </td>
                  <td
                    className="hidden px-4 py-4 font-mono text-xs text-on-surface-variant md:table-cell"
                    title={result.commitId}
                  >
                    {formatCommit(result.commitId)}
                  </td>
                  <td className="hidden max-w-sm px-4 py-4 text-on-surface-variant lg:table-cell">
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
    </Card>
  );
}

function hasPendingPublish(statuses: Array<string | undefined> | undefined) {
  return Boolean(statuses?.some((status) => status === "pending_publish"));
}

function ActionMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <StackNotice title={title} tone="error">
        {message}
      </StackNotice>
    </section>
  );
}
