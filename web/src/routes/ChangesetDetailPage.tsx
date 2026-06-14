import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent } from "react";

import type { Changeset, DiffChangesetResponse } from "../api/types";
import { useApi } from "../api/useApi";
import { cn } from "../lib/cn";

interface DiffFileSection {
  additions: number;
  deletions: number;
  id: string;
  lines: string[];
  path: string;
}

interface DiffSectionDraft {
  diffPath?: string;
  lines: string[];
  newPath?: string;
  oldPath?: string;
}

export function ChangesetDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as { id?: string };
  const changesetId = params.id ?? "";
  const [abandonReason, setAbandonReason] = useState("");
  const [actionError, setActionError] = useState("");

  const changesetQuery = useQuery({
    enabled: Boolean(changesetId),
    queryKey: ["changeset", changesetId],
    queryFn: () => api.getChangeset({ changesetId }),
    refetchInterval: (query) =>
      query.state.data?.status === "pending_publish" ? 2500 : false
  });

  const changeset = changesetQuery.data;
  const canonicalChangesetId = changeset?.id || changesetId;

  const diffQuery = useQuery({
    enabled: Boolean(changeset && canonicalChangesetId),
    queryKey: ["changesetDiff", changesetId],
    queryFn: () => api.diffChangeset({ changesetId: canonicalChangesetId })
  });

  const diffSections = useMemo(
    () =>
      parseUnifiedDiff(
        diffQuery.data?.diff ?? "",
        diffQuery.data?.changedPaths ?? []
      ),
    [diffQuery.data?.changedPaths, diffQuery.data?.diff]
  );

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["changeset", changesetId]
    });
  };

  const approveMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.approveChangeset({ changesetId: canonicalChangesetId });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      await invalidateChangeset();
    }
  });

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }
      if (!changeset?.currentPatchsetId) {
        throw new Error("This changeset has no current patchset to merge.");
      }

      return api.submitChangeset({
        changesetId: canonicalChangesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      await invalidateChangeset();
    }
  });

  const abandonMutation = useMutation({
    mutationFn: async () => {
      if (!canonicalChangesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.abandonChangeset({
        changesetId: canonicalChangesetId,
        reason: abandonReason.trim()
      });
    },
    onError: (error) => setActionError(errorMessage(error)),
    onMutate: () => setActionError(""),
    onSuccess: async () => {
      setActionError("");
      setAbandonReason("");
      await invalidateChangeset();
    }
  });

  const submitAbandon = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    abandonMutation.mutate();
  };

  if (!changesetId) {
    return (
      <PageMessage
        title="Missing changeset"
        message="No changeset id was provided."
      />
    );
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
    return (
      <PageMessage
        title="Changeset not found"
        message="The API returned no changeset."
      />
    );
  }

  const terminal = isTerminalStatus(changeset.status);
  const actionBusy =
    approveMutation.isPending ||
    mergeMutation.isPending ||
    abandonMutation.isPending;

  return (
    <section className="mx-auto w-full max-w-5xl">
      <HeaderCard
        abandonReason={abandonReason}
        actionBusy={actionBusy}
        actionError={actionError}
        abandonPending={abandonMutation.isPending}
        approvePending={approveMutation.isPending}
        changeset={changeset}
        mergePending={mergeMutation.isPending}
        onAbandon={submitAbandon}
        onAbandonReasonChange={setAbandonReason}
        onApprove={() => approveMutation.mutate()}
        onMerge={() => mergeMutation.mutate()}
        terminal={terminal}
      />

      <DiffReview
        diffResponse={diffQuery.data}
        error={diffQuery.error}
        isError={diffQuery.isError}
        isLoading={diffQuery.isPending}
        sections={diffSections}
      />
    </section>
  );
}

function HeaderCard({
  abandonPending,
  abandonReason,
  actionBusy,
  actionError,
  approvePending,
  changeset,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onApprove,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  actionError: string;
  approvePending: boolean;
  changeset: Changeset;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onApprove(): void;
  onMerge(): void;
  terminal: boolean;
}) {
  const sliceSearch = changesetSliceSearch(changeset);

  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="px-5 py-5 md:px-6">
        {sliceSearch ? (
          <Link
            className="text-sm font-medium text-slate-600 underline decoration-slate-300 underline-offset-4 transition hover:text-zinc-950 hover:decoration-slate-700"
            search={{ slice: sliceSearch } as never}
            to="/changesets"
          >
            Back to {sliceSearch} changesets
          </Link>
        ) : null}

        <div className="mt-4 flex flex-col gap-5 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0">
            <h1 className="text-2xl font-semibold tracking-normal text-zinc-950 md:text-3xl">
              {changeset.title || "Untitled changeset"}
            </h1>
            <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 text-sm text-slate-600">
              <span className="rounded-md bg-slate-100 px-2 py-1 font-mono text-xs text-slate-700">
                {changesetHandle(changeset)}
              </span>
              <span>{changeset.author || "author not returned"}</span>
              <StatusBadge status={changeset.status} />
            </div>
            {changeset.description ? (
              <p className="mt-4 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-slate-700">
                {changeset.description}
              </p>
            ) : null}
            {changeset.baseCommitId ? (
              <p className="mt-4 font-mono text-xs text-slate-500">
                base {shortCommit(changeset.baseCommitId)}
              </p>
            ) : null}
          </div>

          <ReviewActions
            abandonPending={abandonPending}
            abandonReason={abandonReason}
            actionBusy={actionBusy}
            approvePending={approvePending}
            canMerge={Boolean(changeset.currentPatchsetId)}
            mergePending={mergePending}
            onAbandon={onAbandon}
            onAbandonReasonChange={onAbandonReasonChange}
            onApprove={onApprove}
            onMerge={onMerge}
            terminal={terminal}
          />
        </div>

        {changeset.submitBlockedReason ? (
          <div className="mt-5 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900">
            {changeset.submitBlockedReason}
          </div>
        ) : null}
        {actionError ? (
          <ErrorBox className="mt-5" message={actionError} />
        ) : null}
      </div>
    </div>
  );
}

function ReviewActions({
  abandonPending,
  abandonReason,
  actionBusy,
  approvePending,
  canMerge,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onApprove,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  approvePending: boolean;
  canMerge: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onApprove(): void;
  onMerge(): void;
  terminal: boolean;
}) {
  return (
    <div className="w-full shrink-0 space-y-3 lg:w-auto lg:min-w-80">
      <div className="flex flex-wrap justify-start gap-2 lg:justify-end">
        <button
          className={secondaryButtonClass}
          disabled={actionBusy || terminal}
          onClick={onApprove}
          type="button"
        >
          {approvePending ? "Approving..." : "Approve"}
        </button>
        <button
          className={primaryButtonClass}
          disabled={actionBusy || terminal || !canMerge}
          onClick={onMerge}
          type="button"
        >
          {mergePending ? "Merging..." : "Merge"}
        </button>
      </div>

      {!terminal ? (
        <form
          className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"
          onSubmit={onAbandon}
        >
          <label className="grid gap-1 text-xs font-medium text-slate-600">
            Reason
            <input
              className="h-9 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
              disabled={actionBusy}
              onChange={(event) => onAbandonReasonChange(event.target.value)}
              placeholder="Optional reason"
              value={abandonReason}
            />
          </label>
          <button
            className={dangerButtonClass}
            disabled={actionBusy}
            type="submit"
          >
            {abandonPending ? "Abandoning..." : "Abandon"}
          </button>
        </form>
      ) : null}
    </div>
  );
}

function DiffReview({
  diffResponse,
  error,
  isError,
  isLoading,
  sections
}: {
  diffResponse?: DiffChangesetResponse;
  error: unknown;
  isError: boolean;
  isLoading: boolean;
  sections: DiffFileSection[];
}) {
  const changedPaths = diffResponse?.changedPaths ?? [];
  const diff = diffResponse?.diff ?? "";
  const hasTextualDiff = diff.trim().length > 0 && sections.length > 0;

  return (
    <section className="mt-6 rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="border-b border-slate-200 px-5 py-4 md:px-6">
        <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div>
            <h2 className="text-base font-semibold tracking-normal text-zinc-950">
              Changed files
            </h2>
            <p className="mt-1 text-sm text-slate-600">
              {isLoading
                ? "Loading file changes..."
                : `${changedPaths.length} file(s) changed`}
            </p>
          </div>
          {sections.length > 0 ? (
            <nav className="flex max-w-full flex-wrap gap-2 md:justify-end">
              {sections.map((section) => (
                <a
                  className="max-w-full truncate rounded-md border border-slate-200 bg-slate-50 px-2.5 py-1 font-mono text-xs text-slate-700 transition hover:border-slate-300 hover:bg-white hover:text-zinc-950"
                  href={`#${section.id}`}
                  key={section.id}
                >
                  {section.path}
                </a>
              ))}
            </nav>
          ) : null}
        </div>
      </div>

      {isLoading ? (
        <DiffSkeleton />
      ) : isError ? (
        <div className="p-5 md:p-6">
          <ErrorBox message={errorMessage(error)} />
        </div>
      ) : hasTextualDiff ? (
        <div className="grid gap-4 p-4 md:p-5">
          {sections.map((section) => (
            <DiffFilePanel key={section.id} section={section} />
          ))}
        </div>
      ) : (
        <div className="p-5 md:p-6">
          <div className="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-600">
            No textual changes to display.
          </div>
        </div>
      )}
    </section>
  );
}

function DiffFilePanel({ section }: { section: DiffFileSection }) {
  return (
    <article
      className="overflow-hidden rounded-lg border border-slate-200 bg-white"
      id={section.id}
    >
      <div className="flex flex-col gap-2 border-b border-slate-200 bg-slate-50 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <h3 className="min-w-0 truncate font-mono text-sm font-semibold text-zinc-950">
          {section.path}
        </h3>
        <div className="flex shrink-0 gap-2 font-mono text-xs">
          <span className="rounded bg-emerald-50 px-2 py-1 text-emerald-800">
            +{section.additions}
          </span>
          <span className="rounded bg-rose-50 px-2 py-1 text-rose-800">
            -{section.deletions}
          </span>
        </div>
      </div>
      <pre className="overflow-x-auto bg-white text-xs leading-5 md:text-sm">
        <code className="block min-w-full py-2">
          {section.lines.map((line, index) => (
            <span
              className={cn(
                "block min-h-5 whitespace-pre px-4 py-0.5",
                diffLineClass(line)
              )}
              key={`${index}-${line}`}
            >
              {line || " "}
            </span>
          ))}
        </code>
      </pre>
    </article>
  );
}

function ErrorBox({
  className,
  message
}: {
  className?: string;
  message: string;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800",
        className
      )}
    >
      {message}
    </div>
  );
}

function PageMessage({ message, title }: { message: string; title: string }) {
  return (
    <section className="mx-auto w-full max-w-5xl">
      <div className="rounded-lg border border-slate-200 bg-white p-6 shadow-sm shadow-slate-200/50">
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
    <section className="mx-auto w-full max-w-5xl">
      <div className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
        <div className="h-4 w-48 animate-pulse rounded bg-slate-200" />
        <div className="mt-5 h-8 w-2/3 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 flex gap-2">
          <div className="h-7 w-24 animate-pulse rounded bg-slate-100" />
          <div className="h-7 w-32 animate-pulse rounded bg-slate-100" />
          <div className="h-7 w-20 animate-pulse rounded bg-slate-100" />
        </div>
      </div>
    </section>
  );
}

function DiffSkeleton() {
  return (
    <div className="grid gap-4 p-4 md:p-5">
      {Array.from({ length: 2 }).map((_, index) => (
        <div
          className="overflow-hidden rounded-lg border border-slate-200"
          key={index}
        >
          <div className="border-b border-slate-200 bg-slate-50 px-4 py-3">
            <div className="h-4 w-64 animate-pulse rounded bg-slate-200" />
          </div>
          <div className="space-y-2 p-4">
            <div className="h-4 w-full animate-pulse rounded bg-slate-100" />
            <div className="h-4 w-11/12 animate-pulse rounded bg-slate-100" />
            <div className="h-4 w-4/5 animate-pulse rounded bg-slate-100" />
            <div className="h-4 w-2/3 animate-pulse rounded bg-slate-100" />
          </div>
        </div>
      ))}
    </div>
  );
}

function StatusBadge({ status }: { status?: string }) {
  return (
    <span
      className={cn(
        "inline-flex rounded-md border px-2 py-1 text-xs font-semibold",
        statusClass(status)
      )}
    >
      {status || "unknown"}
    </span>
  );
}

function parseUnifiedDiff(diff: string, changedPaths: string[]) {
  const normalized = diff.replace(/\r\n/g, "\n");
  if (!normalized.trim()) {
    return [];
  }

  const lines = normalized.split("\n");
  if (lines[lines.length - 1] === "") {
    lines.pop();
  }

  const drafts: DiffSectionDraft[] = [];
  let current: DiffSectionDraft | null = null;

  const startSection = () => {
    current = { lines: [] };
    drafts.push(current);
  };

  lines.forEach((line, index) => {
    const nextLine = lines[index + 1] ?? "";
    const startsGitFile = line.startsWith("diff --git ");
    const startsUnifiedFile =
      line.startsWith("--- ") &&
      nextLine.startsWith("+++ ") &&
      (!current || sectionHasBody(current));

    if (startsGitFile || startsUnifiedFile || !current) {
      startSection();
    }

    if (!current) {
      return;
    }

    updateSectionPath(current, line);
    current.lines.push(line);
  });

  return drafts
    .filter((section) => section.lines.length > 0)
    .map((section, index) => {
      const path =
        section.newPath ||
        section.oldPath ||
        section.diffPath ||
        changedPaths[index] ||
        `File ${index + 1}`;

      return {
        additions: section.lines.filter(isAdditionLine).length,
        deletions: section.lines.filter(isDeletionLine).length,
        id: `diff-file-${index + 1}`,
        lines: section.lines,
        path
      };
    });
}

function updateSectionPath(section: DiffSectionDraft, line: string) {
  if (line.startsWith("diff --git ")) {
    section.diffPath = parseDiffGitPath(line);
    return;
  }

  if (line.startsWith("--- ")) {
    section.oldPath = parseFileHeaderPath(line.slice(4));
    return;
  }

  if (line.startsWith("+++ ")) {
    section.newPath = parseFileHeaderPath(line.slice(4));
  }
}

function parseDiffGitPath(line: string) {
  const match = line.match(/^diff --git a\/(.+) b\/(.+)$/);
  if (!match) {
    return undefined;
  }
  return match[2] || match[1];
}

function parseFileHeaderPath(value: string) {
  const path = value.trim().split("\t")[0];
  if (!path || path === "/dev/null") {
    return undefined;
  }
  return path.replace(/^[ab]\//, "");
}

function sectionHasBody(section: DiffSectionDraft) {
  return section.lines.some(
    (line) =>
      line.startsWith("@@") || isAdditionLine(line) || isDeletionLine(line)
  );
}

function diffLineClass(line: string) {
  if (line.startsWith("@@")) {
    return "bg-sky-50 text-sky-700";
  }
  if (isFileMetadataLine(line)) {
    return "bg-slate-50 text-slate-500";
  }
  if (isAdditionLine(line)) {
    return "bg-emerald-50 text-emerald-900";
  }
  if (isDeletionLine(line)) {
    return "bg-rose-50 text-rose-900";
  }
  return "text-slate-700";
}

function isFileMetadataLine(line: string) {
  return (
    line.startsWith("diff --git ") ||
    line.startsWith("index ") ||
    line.startsWith("--- ") ||
    line.startsWith("+++ ") ||
    line.startsWith("new file mode ") ||
    line.startsWith("deleted file mode ") ||
    line.startsWith("similarity index ") ||
    line.startsWith("rename from ") ||
    line.startsWith("rename to ")
  );
}

function isAdditionLine(line: string) {
  return line.startsWith("+") && !line.startsWith("+++");
}

function isDeletionLine(line: string) {
  return line.startsWith("-") && !line.startsWith("---");
}

function changesetSliceSearch(changeset: Changeset) {
  const ref = changeset.authoringSlice;
  if (!ref?.account || !ref.slice) {
    return "";
  }
  return `${ref.account}/${ref.slice}`;
}

function changesetHandle(changeset: Changeset) {
  if (changeset.handle) {
    return changeset.handle;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "changeset";
}

function shortCommit(commitId: string) {
  return commitId.slice(0, 12);
}

function statusClass(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "merged":
    case "submitted":
      return "border-emerald-200 bg-emerald-50 text-emerald-800";
    case "pending_publish":
      return "border-amber-200 bg-amber-50 text-amber-900";
    case "abandoned":
      return "border-rose-200 bg-rose-50 text-rose-800";
    case "draft":
      return "border-slate-200 bg-slate-50 text-slate-700";
    default:
      return "border-slate-200 bg-slate-50 text-slate-700";
  }
}

function isTerminalStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "merged" || normalized === "abandoned";
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

const primaryButtonClass =
  "rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";

const secondaryButtonClass =
  "rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-medium text-zinc-800 transition hover:border-zinc-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";

const dangerButtonClass =
  "self-end rounded-md border border-rose-300 bg-white px-4 py-2.5 text-sm font-medium text-rose-700 transition hover:border-rose-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";
