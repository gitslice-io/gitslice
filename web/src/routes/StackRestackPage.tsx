import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { Changeset, PatchsetConflict } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import {
  Badge,
  Button,
  Card,
  Input,
  PageHeader,
  surfaceClassName
} from "../components/ui";
import { cn } from "../lib/cn";
import { shortChangesetId } from "../lib/objectId";
import {
  affectedSubtreeEntries,
  changedPathCount,
  conflictCount,
  currentPatchset,
  currentPatchsetNumber,
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
            { label: "Update" }
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
        description="Preview the dependent changesets that will be replayed, then create updated patchsets through the server."
        eyebrow="Update dependents"
        title={<span className="font-serif">{stackDisplayName(stack)}</span>}
      />

      <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <div className="min-w-0 space-y-6">
          <RestackPreview entries={previewEntries} />
          <RestackResult entries={restackMutation.data?.entries ?? []} />
          {restackMutation.isError ? (
            <StackNotice title="Update failed" tone="error">
              {getErrorMessage(restackMutation.error)}
            </StackNotice>
          ) : null}
        </div>

        <form
          className={cn(surfaceClassName({ level: "low" }), "grid content-start gap-4 p-5")}
          onSubmit={submit}
        >
          <h2 className="text-sm font-semibold text-on-surface">Update options</h2>
          <label className="grid gap-2 font-label text-sm font-semibold text-on-surface">
            Update from
            <select
              className={nativeControlClassName}
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
          <label className="flex items-start gap-2 font-label text-sm font-semibold text-on-surface">
            <input
              checked={includeSiblings}
              className="mt-1 h-4 w-4 rounded-sm bg-white text-primary ring-1 ring-outline-variant/15 focus:ring-2 focus:ring-primary"
              onChange={(event) => setIncludeSiblings(event.target.checked)}
              type="checkbox"
            />
            Include sibling subtrees
          </label>
          <label className="grid gap-2 font-label text-sm font-semibold text-on-surface">
            Target base commit
            <Input
              onChange={(event) => setTargetBaseCommitId(event.target.value)}
              placeholder={formatCommit(stack.baseCommitId)}
              value={targetBaseCommitId}
            />
          </label>
          <Button
            disabled={!defaultStartId || restackMutation.isPending}
            type="submit"
          >
            {restackMutation.isPending ? "Updating..." : "Update dependents"}
          </Button>
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
      <StackNotice title="No affected changesets">
        This dependency tree has no changeset that can be updated from the selected point.
      </StackNotice>
    );
  }

  return (
    <Card level="low" padding="none">
      <div className="bg-surface-container-high px-4 py-3">
        <h2 className="text-sm font-semibold text-on-surface">Affected changesets</h2>
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
            </div>
          </div>
        ))}
      </div>
    </Card>
  );
}

function RestackResult({ entries }: { entries: Changeset[] }) {
  if (!entries.length) {
    return (
      <StackNotice title="No update result yet">
        Run update to create new patchsets or confirm that selected changesets are unchanged.
      </StackNotice>
    );
  }

  const conflictRows = entries.flatMap((changeset) =>
    (currentPatchset(changeset)?.conflicts ?? []).map((conflict) => ({
      changeset,
      conflict
    }))
  );

  return (
    <Card level="low" padding="none">
      <div className="bg-surface-container-high px-4 py-3">
        <h2 className="text-sm font-semibold text-on-surface">Update result</h2>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full text-left text-sm">
          <thead className="bg-surface-container font-label text-xs font-semibold uppercase tracking-normal text-on-surface-variant">
            <tr>
              <th className="px-4 py-3">Changeset</th>
              <th className="px-4 py-3">Result</th>
              <th className="hidden px-4 py-3 md:table-cell">Patchset</th>
              <th className="hidden px-4 py-3 lg:table-cell">Paths</th>
              <th className="px-4 py-3 text-right">Open</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((changeset) => {
              const conflicts = conflictCount(changeset);
              const displayStatus = conflicts > 0 ? "conflict" : changeset.status || "clean";
              const detailId = shortChangesetId(changeset.id || "") || changeset.id || "";

              return (
                <tr
                  className="align-top odd:bg-surface-container-lowest even:bg-surface-container-low"
                  key={changeset.id}
                >
                  <td className="px-4 py-4">
                    <span className="font-semibold text-on-surface">
                      {changeset.handle || detailId || "changeset"}
                    </span>
                    <p className="mt-1 break-words text-sm text-on-surface-variant">
                      {changeset.title || "Untitled changeset"}
                    </p>
                  </td>
                  <td className="px-4 py-4">
                    <StackStatusBadge status={displayStatus} />
                  </td>
                  <td className="hidden px-4 py-4 text-on-surface-variant md:table-cell">
                    {currentPatchsetNumber(changeset) || "none"}
                  </td>
                  <td className="hidden px-4 py-4 text-on-surface-variant lg:table-cell">
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
        <div className="bg-rose-100/70 px-4 py-4">
          <h3 className="text-sm font-semibold text-rose-900">
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
    </Card>
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
    <Card level="lowest" padding="sm">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="break-all font-mono text-sm font-semibold text-on-surface">
            {conflict.path || "path not returned"}
          </div>
          <div className="mt-1 flex flex-wrap gap-2 text-xs text-on-surface-variant">
            <Badge className="bg-rose-100 text-rose-800" variant="neutral">
              {conflict.conflictClass === "restack"
                ? "base update"
                : conflict.conflictClass || "conflict"}
            </Badge>
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
      <dl className="mt-3 grid gap-2 text-xs text-on-surface-variant sm:grid-cols-2">
        <ConflictMetadata label="Old base" value={conflict.oldBaseCommitId} />
        <ConflictMetadata label="New base" value={conflict.newBaseCommitId} />
        <ConflictMetadata label="Base fingerprint" value={conflict.baseFingerprint} />
        <ConflictMetadata label="Remote fingerprint" value={conflict.remoteFingerprint} />
      </dl>
    </Card>
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
      <dt className="font-label font-semibold text-on-surface-muted">{label}</dt>
      <dd className="break-all font-mono text-on-surface">{value || "not returned"}</dd>
    </div>
  );
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
