import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";

import type { Changeset } from "../../api/types";
import { cn } from "../../lib/cn";
import { useMobileDetails } from "../../components/MobileDetails";
import { shortChangesetId } from "../../lib/objectId";
import { displaySubmitBlockedReason } from "../stackPageUtils";
import { isMergeableStatus, isPublishing } from "./status";
import { StatusBadge } from "./StatusBadge";
import { ChangesetMetaLine } from "./ChangesetMetaLine";
import { CopyLinkButton } from "./CopyLinkButton";
import { ErrorBox } from "./ErrorBox";
import { ReviewActions } from "./ReviewActions";
import { changesetLabel } from "./status";

export function HeaderCard({
  abandonPending,
  abandonReason,
  actionBusy,
  actionError,
  changeset,
  dependentChangesets,
  canUseReviewActions,
  mergePending,
  onAbandon,
  onAbandonReasonChange,
  onMerge,
  patchsetCompare,
  terminal
}: {
  abandonPending: boolean;
  abandonReason: string;
  actionBusy: boolean;
  actionError: string;
  changeset: Changeset;
  dependentChangesets: { id: string; title: string }[];
  canUseReviewActions: boolean;
  mergePending: boolean;
  onAbandon(event: FormEvent<HTMLFormElement>): void;
  onAbandonReasonChange(value: string): void;
  onMerge(): void;
  patchsetCompare?: ReactNode;
  terminal: boolean;
}) {
  // On phones the page folds its details; outside such a page the card keeps
  // its own toggle for the description.
  const pageDetails = useMobileDetails();
  const [localDetails, setLocalDetails] = useState(false);
  const showDetails = pageDetails ? pageDetails.open : localDetails;
  const toggleDetails = pageDetails
    ? pageDetails.toggle
    : () => setLocalDetails((value) => !value);
  const folded = showDetails ? "" : "max-lg:hidden";
  const publishing = isPublishing(changeset.status);
  const hasExpandableContent = Boolean(
    pageDetails || changeset.description || changeset.baseCommitId
  );

  return (
    <div className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
      <div className="px-3 py-2.5 md:px-5 md:py-3">
        {publishing ? (
          <div className="mb-2.5 flex items-center gap-2 rounded-md border border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 px-2.5 py-1.5 text-xs font-medium text-amber-900 dark:text-amber-200 md:mb-3 md:px-3 md:py-2 md:text-sm">
            <span
              aria-hidden="true"
              className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500"
            />
            Publishing changeset…
          </div>
        ) : null}
        <div className="flex flex-col gap-2.5 lg:flex-row lg:items-start lg:justify-between lg:gap-3">
          <div className="min-w-0">
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 md:gap-3">
              <h1
                className={cn(
                  "truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg md:text-xl",
                  // Unfolded on a phone, the whole title shows.
                  showDetails && "max-lg:whitespace-normal max-lg:break-words"
                )}
                title={changeset.title || "Untitled changeset"}
              >
                {changeset.title || "Untitled changeset"}
              </h1>
              {hasExpandableContent ? (
                <button
                  aria-expanded={showDetails}
                  aria-label={showDetails ? "Hide details" : "Show details"}
                  className="shrink-0 rounded-md border border-slate-200 dark:border-zinc-800 px-2.5 py-1 text-xs font-medium text-slate-600 dark:text-zinc-400 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:text-zinc-950 dark:hover:text-zinc-50 lg:hidden"
                  onClick={toggleDetails}
                  type="button"
                >
                  {showDetails ? "Hide" : "Details"}
                </button>
              ) : null}
            </div>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[11px] text-slate-600 dark:text-zinc-400 md:mt-2 md:gap-x-2 md:gap-y-1.5 md:text-xs">
              <span
                className={cn(
                  "max-w-[12rem] truncate rounded bg-slate-100 dark:bg-zinc-800 px-1.5 py-0.5 font-mono text-slate-700 dark:text-zinc-300 md:px-2 md:py-1",
                  folded
                )}
                title={changesetLabel(changeset)}
              >
                {changesetLabel(changeset)}
              </span>
              <StatusBadge status={changeset.status} />
              {changeset.parentChangesetId ? (
                <Link
                  className={cn("inline-flex max-w-[12rem] items-center gap-1 truncate rounded border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-1.5 py-0.5 font-medium text-slate-600 dark:text-zinc-400 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:text-zinc-950 dark:hover:text-zinc-50 md:max-w-[14rem] md:px-2 md:py-1", folded)}
                  params={{
                    id:
                      shortChangesetId(changeset.parentChangesetId) ||
                      changeset.parentChangesetId
                  }}
                  title={`Base changeset ${changeset.parentChangesetId}`}
                  to="/cs/$id"
                >
                  <span className="shrink-0">Base changeset</span>
                  <span className="truncate font-mono">
                    {shortChangesetId(changeset.parentChangesetId) ||
                      changeset.parentChangesetId}
                  </span>
                </Link>
              ) : null}
              {dependentChangesets.map((dependent) => (
                <Link
                  className={cn("inline-flex max-w-[12rem] items-center gap-1 truncate rounded border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-1.5 py-0.5 font-medium text-slate-600 dark:text-zinc-400 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:text-zinc-950 dark:hover:text-zinc-50 md:max-w-[14rem] md:px-2 md:py-1", folded)}
                  key={dependent.id}
                  params={{ id: shortChangesetId(dependent.id) || dependent.id }}
                  title={
                    dependent.title
                      ? `Dependent changeset ${dependent.id} — ${dependent.title}`
                      : `Dependent changeset ${dependent.id}`
                  }
                  to="/cs/$id"
                >
                  <span className="shrink-0">Dependent</span>
                  <span className="truncate font-mono">
                    {shortChangesetId(dependent.id) || dependent.id}
                  </span>
                </Link>
              ))}
              <span className={cn("contents", folded)}>
                <CopyLinkButton changesetId={changeset.id || ""} />
                <ChangesetMetaLine changeset={changeset} />
              </span>
            </div>
            <div className={cn("lg:block", showDetails ? "block" : "hidden")}>
              {changeset.description ? (
                <p className="mt-3 max-w-3xl whitespace-pre-wrap text-sm leading-6 text-slate-700 dark:text-zinc-300 md:mt-4">
                  {changeset.description}
                </p>
              ) : null}
              {changeset.baseCommitId ? (
                <p
                  className="mt-3 font-mono text-xs text-slate-500 dark:text-zinc-400 md:mt-4"
                  title={changeset.baseCommitId}
                >
                  base {changeset.baseCommitId.slice(0, 12)}
                </p>
              ) : null}
            </div>
          </div>

          {canUseReviewActions ? (
            <div className="w-full shrink-0 lg:w-auto">
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
          ) : null}
        </div>

        {changeset.submitBlockedReason ? (
          <div className={cn("mt-2.5 rounded-md border border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 px-3 py-2 text-xs text-amber-900 dark:text-amber-200 md:mt-3 md:text-sm", folded)}>
            {displaySubmitBlockedReason(changeset.submitBlockedReason)}
          </div>
        ) : null}
        {actionError ? (
          <ErrorBox className="mt-4 md:mt-5" message={actionError} />
        ) : null}
      </div>
      {patchsetCompare ? (
        <div className={cn("border-t border-slate-100 dark:border-zinc-800 px-3 py-2 md:px-5 md:py-2.5", folded)}>
          {patchsetCompare}
        </div>
      ) : null}
    </div>
  );
}