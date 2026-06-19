import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams, useSearch } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import type { Changeset } from "../api/types";
import { useApi } from "../api/useApi";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import { DiffViewer } from "../components/diff/DiffViewer";
import { cn } from "../lib/cn";
import { shortChangesetId, shortHash } from "../lib/objectId";
import { displaySubmitBlockedReason, shortStackId } from "./stackPageUtils";

interface ChangesetSearch {
  dependency?: unknown;
}

export function ChangesetDetailPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const params = useParams({ strict: false }) as { id?: string };
  const search = useSearch({ strict: false }) as ChangesetSearch;
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
  const authoringAccount = changeset?.authoringSlice?.account ?? "";
  const authoringSlice = changeset?.authoringSlice?.slice ?? "";
  const sliceSearch = changeset ? changesetSliceSearch(changeset) : "";
  const stackId =
    changeset?.stackId ||
    (typeof search.dependency === "string" ? search.dependency.trim() : "");

  const resolveSliceQuery = useQuery({
    enabled: Boolean(authoringAccount && authoringSlice),
    queryKey: ["resolveSlice", authoringAccount, authoringSlice],
    queryFn: () =>
      api.resolveSlice({
        ref: { account: authoringAccount, slice: authoringSlice }
      })
  });

  const diffQuery = useQuery({
    enabled: Boolean(changeset && canonicalChangesetId),
    queryKey: ["changesetDiff", changesetId],
    queryFn: () => api.diffChangeset({ changesetId: canonicalChangesetId })
  });

  const invalidateChangeset = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["changeset", changesetId]
    });
  };

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
  const actionBusy = mergeMutation.isPending || abandonMutation.isPending;
  const resolvedSliceId = resolveSliceQuery.data?.id ?? "";

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-5">
        <Breadcrumb
          items={changesetBreadcrumbItems({
            changeset,
            resolvedSliceId,
            sliceSearch,
            stackId
          })}
        />
      </div>

      <HeaderCard
        abandonReason={abandonReason}
        actionBusy={actionBusy}
        actionError={actionError}
        abandonPending={abandonMutation.isPending}
        changeset={changeset}
        mergePending={mergeMutation.isPending}
        onAbandon={submitAbandon}
        onAbandonReasonChange={setAbandonReason}
        onMerge={() => mergeMutation.mutate()}
        stackId={stackId}
        terminal={terminal}
      />

      <DiffViewer
        diffResponse={diffQuery.data}
        error={diffQuery.error}
        isError={diffQuery.isError}
        isLoading={diffQuery.isPending}
      />
    </section>
  );
}

function HeaderCard({
  abandonPending,
  abandonReason,
  actionBusy,
  actionError,
  changeset,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  stackId,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  actionError: string;
  changeset: Changeset;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  stackId: string;
  terminal: boolean;
}) {
  // On small screens the diff is the priority, so the description, base commit,
  // and review actions collapse behind a toggle. From `lg` up everything is
  // always shown in the original two-column layout (the toggle is hidden).
  const [showDetails, setShowDetails] = useState(false);

  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="px-5 py-4 md:px-6 md:py-5">
        <div className="flex flex-col gap-5 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0">
            <div className="flex items-start justify-between gap-3">
              <h1 className="text-xl font-semibold tracking-normal text-zinc-950 sm:text-2xl md:text-3xl">
                {changeset.title || "Untitled changeset"}
              </h1>
              <button
                aria-expanded={showDetails}
                className="mt-1 shrink-0 rounded-md border border-slate-200 px-2.5 py-1 text-xs font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950 lg:hidden"
                onClick={() => setShowDetails((value) => !value)}
                type="button"
              >
                {showDetails ? "Hide details" : "Details"}
              </button>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 text-sm text-slate-600 md:mt-3">
              <span className="max-w-full break-all rounded-md bg-slate-100 px-2 py-1 font-mono text-xs text-slate-700">
                {changesetLabel(changeset)}
              </span>
              <span>{changeset.author || "author not returned"}</span>
              <StatusBadge status={changeset.status} />
              {stackId ? (
                <Link
                  className="inline-flex items-center rounded-md border border-slate-200 bg-white px-2 py-1 text-xs font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950"
                  params={{ id: stackId }}
                  to="/dependencies/$id"
                >
                  Dependencies {shortStackId(stackId) || stackId}
                </Link>
              ) : null}
              <CopyLinkButton changesetId={changeset.id || ""} />
            </div>
            <div className={cn("lg:block", showDetails ? "block" : "hidden")}>
              {changeset.description ? (
                <p className="mt-4 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-slate-700">
                  {changeset.description}
                </p>
              ) : null}
              {changeset.baseCommitId ? (
                <p
                  className="mt-4 font-mono text-xs text-slate-500"
                  title={changeset.baseCommitId}
                >
                  base {shortCommit(changeset.baseCommitId)}
                </p>
              ) : null}
            </div>
          </div>

          <div
            className={cn(
              "w-full shrink-0 lg:block lg:w-auto",
              showDetails ? "block" : "hidden"
            )}
          >
            <ReviewActions
              abandonPending={abandonPending}
              abandonReason={abandonReason}
              actionBusy={actionBusy}
              canMerge={
                Boolean(changeset.currentPatchsetId) &&
                isMergeableStatus(changeset.status)
              }
              mergePending={mergePending}
              onAbandon={onAbandon}
              onAbandonReasonChange={onAbandonReasonChange}
              onMerge={onMerge}
              terminal={terminal}
            />
          </div>
        </div>

        {changeset.submitBlockedReason ? (
          <div className="mt-5 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900">
            {displaySubmitBlockedReason(changeset.submitBlockedReason)}
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
  canMerge,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  canMerge: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  terminal: boolean;
}) {
  return (
    <div className="w-full shrink-0 space-y-3 lg:w-auto lg:min-w-80">
      <div className="flex flex-wrap justify-start gap-2 lg:justify-end">
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
              className="h-9 min-w-0 rounded-md border border-slate-300 bg-white px-3 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100"
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
    <section className="mx-auto w-full max-w-[100rem]">
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
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
        <div className="h-4 w-48 animate-pulse rounded bg-slate-200" />
        <div className="mt-5 h-8 w-2/3 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 flex flex-wrap gap-2">
          <div className="h-7 w-24 animate-pulse rounded bg-slate-100" />
          <div className="h-7 w-32 animate-pulse rounded bg-slate-100" />
          <div className="h-7 w-20 animate-pulse rounded bg-slate-100" />
        </div>
      </div>
    </section>
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

// Copies the short, shareable /cs/<id> URL to the clipboard so reviewers can
// paste it around. Falls back silently when the clipboard API is unavailable.
function CopyLinkButton({ changesetId }: { changesetId: string }) {
  const [copied, setCopied] = useState(false);
  const shareId = shortChangesetId(changesetId);

  if (!shareId) {
    return null;
  }

  const shareUrl =
    typeof window !== "undefined"
      ? `${window.location.origin}/cs/${shareId}`
      : `/cs/${shareId}`;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(shareUrl);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard can be blocked (insecure origin / permissions); ignore.
    }
  };

  return (
    <button
      className="inline-flex items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2 py-1 text-xs font-medium text-slate-600 transition hover:border-slate-300 hover:text-zinc-950"
      onClick={copy}
      title={shareUrl}
      type="button"
    >
      <span aria-hidden="true">{copied ? "✓" : "🔗"}</span>
      {copied ? "Link copied" : "Copy link"}
    </button>
  );
}

function changesetSliceSearch(changeset: Changeset) {
  const ref = changeset.authoringSlice;
  if (!ref?.account || !ref.slice) {
    return "";
  }
  return `${ref.account}:${ref.slice}`;
}

function changesetBreadcrumbItems({
  changeset,
  resolvedSliceId,
  sliceSearch,
  stackId
}: {
  changeset: Changeset;
  resolvedSliceId: string;
  sliceSearch: string;
  stackId: string;
}): Crumb[] {
  const items: Crumb[] = [{ label: "Slices", to: "/slices" }];

  if (sliceSearch) {
    items.push(
      resolvedSliceId
        ? {
            label: sliceSearch,
            params: { id: resolvedSliceId },
            to: "/slices/$id"
          }
        : { label: sliceSearch }
    );
    items.push({
      label: `${sliceSearch} changesets`,
      search: { slice: sliceSearch },
      to: "/changesets"
    });
  }

  if (stackId) {
    items.push({
      label: `dependencies ${shortStackId(stackId) || stackId}`,
      params: { id: stackId },
      to: "/dependencies/$id"
    });
  }

  items.push({ label: changesetLabel(changeset) });

  return items;
}

function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "changeset";
}

function shortCommit(commitId: string) {
  return shortHash(commitId);
}

function statusClass(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "published":
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

// A changeset that has been submitted/published/abandoned is no longer open for
// action; merge/abandon controls hide for these.
function isTerminalStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return (
    normalized === "submitted" ||
    normalized === "pending_publish" ||
    normalized === "published" ||
    normalized === "merged" ||
    normalized === "abandoned"
  );
}

// Submit (merge) is only valid while the changeset is still open/draft.
function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

const primaryButtonClass =
  "rounded-md bg-zinc-950 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-zinc-800 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";

const dangerButtonClass =
  "self-end rounded-md border border-rose-300 bg-white px-4 py-2.5 text-sm font-medium text-rose-700 transition hover:border-rose-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";
