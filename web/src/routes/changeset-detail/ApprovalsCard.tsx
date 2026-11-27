import { Timestamp } from "../../components/Timestamp";
import { stripPathPrefix } from "../../components/diff/diffViewShared";
import { cn } from "../../lib/cn";
import type { ApprovalSummary } from "./approvalSummary";
import { formatActorAttribution } from "../../components/changesets/ChangesetMetaLine";

export function ApprovalStatusBadge({
  className,
  summary
}: {
  className?: string;
  summary: ApprovalSummary;
}) {
  const toneClass =
    summary.badgeTone === "met" || summary.badgeTone === "approved"
      ? "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-800 dark:text-emerald-200"
      : summary.badgeTone === "needed"
        ? "border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 text-amber-900 dark:text-amber-200"
        : "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-600 dark:text-zinc-400";

  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-[11px] font-semibold md:py-1 md:text-xs",
        toneClass,
        className
      )}
    >
      {summary.badgeTone === "met" || summary.badgeTone === "approved" ? (
        <svg
          aria-hidden="true"
          className="h-3 w-3 shrink-0"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="2.4"
          viewBox="0 0 24 24"
        >
          <polyline points="20 6 9 17 4 12" />
        </svg>
      ) : summary.badgeTone === "needed" ? (
        <span aria-hidden="true" className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" />
      ) : null}
      {summary.badgeLabel}
    </span>
  );
}

interface ApprovalsCardProps {
  approvePending?: boolean;
  canApprove?: boolean;
  embedded?: boolean;
  onApprove?: () => void;
  pathPrefix?: string;
  summary: ApprovalSummary;
  terminal?: boolean;
}

export function ApprovalsCard({
  approvePending = false,
  canApprove = false,
  embedded = false,
  onApprove,
  pathPrefix,
  summary,
  terminal = false
}: ApprovalsCardProps) {
  if (!summary.hasRequirements && summary.currentApprovals.length === 0) {
    return null;
  }

  const showApproveButton = Boolean(
    !embedded && !terminal && canApprove && onApprove && !summary.viewerHasApproved
  );

  const content = (
    <>
      {!embedded ? (
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 dark:border-zinc-800 px-3 py-2.5 md:px-5">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
              Approvals
            </h2>
            {summary.hasRequirements ? (
              <span
                className={cn(
                  "inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] font-semibold md:text-xs",
                  summary.satisfied
                    ? "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-800 dark:text-emerald-200"
                    : "border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 text-amber-900 dark:text-amber-200"
                )}
              >
                {summary.satisfied ? "All requirements met" : "Waiting for approvals"}
              </span>
            ) : (
              <span className="inline-flex items-center rounded-md border border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 px-2 py-0.5 text-[11px] font-semibold text-emerald-800 dark:text-emerald-200 md:text-xs">
                Approved
              </span>
            )}
          </div>
          {showApproveButton ? (
            <button
              className="rounded-md border border-emerald-300 dark:border-emerald-800 bg-emerald-50 dark:bg-emerald-950/40 px-2.5 py-1 text-xs font-semibold text-emerald-900 dark:text-emerald-200 transition hover:bg-emerald-100 dark:hover:bg-emerald-950/70 disabled:cursor-not-allowed disabled:opacity-60"
              disabled={approvePending}
              onClick={onApprove}
              type="button"
            >
              {approvePending ? "Approving..." : "Approve"}
            </button>
          ) : summary.viewerHasApproved ? (
            <span className="text-xs font-medium text-emerald-700 dark:text-emerald-300">
              You approved this patchset
            </span>
          ) : null}
        </div>
      ) : null}

      {summary.requirements.length > 0 ? (
        <ul className="divide-y divide-slate-100 dark:divide-zinc-800">
          {summary.requirements.map((req, index) => {
            const met = Boolean(req.satisfied);
            const detailText = req.detail ? stripPathPrefix(req.detail, pathPrefix) : "";
            return (
              <li
                className={cn(
                  "flex items-start justify-between gap-3 px-3 py-2 md:px-5",
                  !embedded && "py-2.5"
                )}
                key={`${req.kind ?? "req"}-${req.label ?? index}`}
              >
                <div className="flex min-w-0 items-start gap-2">
                  <span
                    aria-hidden="true"
                    className={cn(
                      "mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full border",
                      met
                        ? "border-emerald-500/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300"
                        : "border-amber-500/50 bg-amber-500/10 text-amber-700 dark:text-amber-300"
                    )}
                  >
                    {met ? (
                      <svg
                        className="h-2.5 w-2.5"
                        fill="none"
                        stroke="currentColor"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth="2.8"
                        viewBox="0 0 24 24"
                      >
                        <polyline points="20 6 9 17 4 12" />
                      </svg>
                    ) : (
                      <span className="h-1.5 w-1.5 rounded-full bg-amber-500" />
                    )}
                  </span>
                  <div className="min-w-0">
                    <p className="text-xs font-medium text-zinc-950 dark:text-zinc-50 sm:text-sm">
                      {req.label || "Approval required"}
                    </p>
                    {detailText ? (
                      <p className="mt-0.5 truncate font-mono text-[11px] text-slate-500 dark:text-zinc-400">
                        {detailText}
                      </p>
                    ) : null}
                  </div>
                </div>
                <span
                  className={cn(
                    "shrink-0 font-mono text-xs font-semibold tabular-nums",
                    met
                      ? "text-emerald-700 dark:text-emerald-300"
                      : "text-amber-800 dark:text-amber-200"
                  )}
                >
                  {req.current ?? 0}/{req.required ?? 0}
                </span>
              </li>
            );
          })}
        </ul>
      ) : null}

      {summary.currentApprovals.length > 0 ? (
        <div
          className={cn(
            "flex flex-wrap items-center gap-x-2.5 gap-y-1 px-3 py-2 text-xs text-slate-600 dark:text-zinc-400 md:px-5",
            summary.requirements.length > 0 &&
              "border-t border-slate-100 dark:border-zinc-800 bg-slate-50/50 dark:bg-zinc-950/30"
          )}
        >
          <span className="font-medium text-slate-500 dark:text-zinc-400">Approved by</span>
          {summary.currentApprovals.map((app) => {
            const name =
              formatActorAttribution(app.attribution, app.username || app.subjectId) ||
              app.username ||
              app.subjectId ||
              "Reviewer";
            return (
              <span
                className="inline-flex items-center gap-1 rounded bg-white dark:bg-zinc-900 border border-slate-200/80 dark:border-zinc-800 px-2 py-0.5 font-medium text-zinc-900 dark:text-zinc-100"
                key={`${app.subjectId ?? name}-${app.patchsetId ?? ""}`}
              >
                <span>{name}</span>
                {app.createdAt ? (
                  <span className="font-normal text-slate-400 dark:text-zinc-500">
                    · <Timestamp relative value={app.createdAt} />
                  </span>
                ) : null}
              </span>
            );
          })}
        </div>
      ) : null}
    </>
  );

  if (embedded) {
    return <div aria-label="Approvals">{content}</div>;
  }

  return (
    <section
      aria-label="Approvals"
      className="mt-3 rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50"
    >
      {content}
    </section>
  );
}
