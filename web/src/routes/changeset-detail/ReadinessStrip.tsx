import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { RpcError, isDefiniteClientError, isUnauthenticatedError } from "../../api/rpcError";
import type { CheckRun, AutomationRun } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { checkRunsQuery, triggerRunsQuery } from "../../queries/checks";
import { ApprovalsCard } from "./ApprovalsCard";
import type { ApprovalSummary } from "./approvalSummary";
import { groupTriggersAndChecks } from "./automationGrouping";
import { isTerminalCheckStatus } from "../../lib/checkStatus";
import { isFinalTriggerStatus } from "../../components/automations/AutomationRunLog";
import { TriggersChecksPanel } from "./TriggersChecksPanel";

const POLL_MS = 3500;

interface ChecksSummary {
  label: string;
  tone: "passed" | "running" | "failed" | "muted";
  live: boolean;
}

export function summarizeChecksAndTriggers(
  checkRuns: readonly CheckRun[],
  primaryTriggers: readonly { trigger: AutomationRun; checks: CheckRun[] }[],
  signedOut: boolean
): ChecksSummary | null {
  if (signedOut) {
    return { label: "Sign in for checks", tone: "muted", live: false };
  }

  const totalChecks = checkRuns.length;
  const totalTriggers = primaryTriggers.length;
  if (totalChecks === 0 && totalTriggers === 0) {
    return null;
  }

  const failedChecks = checkRuns.filter((run) => {
    const s = (run.status ?? "").toLowerCase();
    return s === "failed" || s === "errored" || s === "timed_out" || s === "canceled";
  }).length;
  const failedTriggers = primaryTriggers.filter(({ trigger }) => {
    const s = (trigger.status ?? "").toLowerCase();
    return s === "failed" || s === "errored" || s === "canceled";
  }).length;
  const runningChecks = checkRuns.filter((run) => !isTerminalCheckStatus(run.status)).length;
  const runningTriggers = primaryTriggers.filter(
    ({ trigger }) => !isFinalTriggerStatus(trigger.status)
  ).length;
  const passedChecks = checkRuns.filter((run) => {
    const s = (run.status ?? "").toLowerCase();
    return s === "passed" || s === "succeeded";
  }).length;

  const live = runningChecks > 0 || runningTriggers > 0;

  if (failedChecks > 0) {
    return {
      label: `${failedChecks}/${totalChecks} check${totalChecks === 1 ? "" : "s"} failed`,
      tone: "failed",
      live
    };
  }
  if (failedTriggers > 0) {
    return {
      label: `${failedTriggers} trigger${failedTriggers === 1 ? "" : "s"} failed`,
      tone: "failed",
      live
    };
  }
  if (live) {
    if (totalChecks > 0) {
      return {
        label: `Checks ${passedChecks}/${totalChecks}`,
        tone: "running",
        live: true
      };
    }
    return { label: "Triggers running", tone: "running", live: true };
  }
  if (totalChecks > 0) {
    return {
      label: `${passedChecks}/${totalChecks} check${totalChecks === 1 ? "" : "s"} passed`,
      tone: "passed",
      live: false
    };
  }
  return {
    label: `${totalTriggers} trigger${totalTriggers === 1 ? "" : "s"} succeeded`,
    tone: "passed",
    live: false
  };
}

export function ReadinessStrip({
  approvalSummary,
  changesetId,
  patchsetId,
  pathPrefix,
  signedOut = false,
  terminal = false
}: {
  approvalSummary: ApprovalSummary;
  changesetId: string;
  patchsetId: string;
  pathPrefix?: string;
  signedOut?: boolean;
  terminal?: boolean;
}) {
  const api = useApi();

  const checkQueryOpts = useMemo(
    () => checkRunsQuery(api, changesetId, patchsetId),
    [api, changesetId, patchsetId]
  );
  const triggerQueryOpts = useMemo(
    () => triggerRunsQuery(api, changesetId),
    [api, changesetId]
  );

  const checksQuery = useQuery({
    ...checkQueryOpts,
    enabled: Boolean(changesetId && patchsetId) && !signedOut,
    retry: (failures, error) => !isUnauthenticatedError(error) && failures < 3,
    refetchInterval: (query) =>
      query.state.data?.some((run) => !isTerminalCheckStatus(run.status)) ? POLL_MS : false
  });

  const triggersQuery = useQuery({
    ...triggerQueryOpts,
    enabled: Boolean(changesetId) && !signedOut,
    retry: (failures, error) =>
      !isDefiniteClientError(error) &&
      !isUnauthenticatedError(error) &&
      !(error instanceof RpcError && error.status === 503) &&
      failures < 3,
    refetchInterval: (query) =>
      query.state.data?.some((run) => !isFinalTriggerStatus(run.status)) ? POLL_MS : false
  });

  const checkRuns = checksQuery.data ?? [];
  const triggerRuns = triggersQuery.isError ? [] : (triggersQuery.data ?? []);
  const grouped = useMemo(
    () => groupTriggersAndChecks(triggerRuns, checkRuns, patchsetId),
    [triggerRuns, checkRuns, patchsetId]
  );

  const checksSignedOut =
    signedOut || (checksQuery.isError && isUnauthenticatedError(checksQuery.error));
  const checksSummary = summarizeChecksAndTriggers(
    checkRuns,
    grouped.primaryTriggers,
    checksSignedOut
  );

  const showApprovals =
    !terminal &&
    (approvalSummary.hasRequirements || approvalSummary.currentApprovals.length > 0);
  const showChecks =
    Boolean(changesetId && patchsetId) &&
    (checksSignedOut ||
      checksQuery.isError ||
      checkRuns.length > 0 ||
      grouped.primaryTriggers.length > 0 ||
      grouped.skippedTriggers.length > 0);

  if (!showApprovals && !showChecks) {
    return null;
  }

  return (
    <details className="readiness-details border-t border-slate-100 dark:border-zinc-800">
      <summary className="flex cursor-pointer select-none items-center justify-between gap-2 px-3 py-2 text-xs transition hover:bg-slate-50/80 dark:hover:bg-zinc-800/40 md:px-5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
          <svg
            aria-hidden="true"
            className="readiness-chevron h-3.5 w-3.5 shrink-0 text-slate-400 dark:text-zinc-500"
            fill="none"
            stroke="currentColor"
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth="2.2"
            viewBox="0 0 16 16"
          >
            <path d="m6 4 4 4-4 4" />
          </svg>

          {showApprovals ? (
            <span
              className={cn(
                "inline-flex items-center gap-1.5 font-medium",
                approvalSummary.badgeTone === "met" || approvalSummary.badgeTone === "approved"
                  ? "text-emerald-700 dark:text-emerald-300"
                  : approvalSummary.badgeTone === "needed"
                    ? "text-amber-800 dark:text-amber-200"
                    : "text-slate-600 dark:text-zinc-400"
              )}
            >
              <StatusGlyph
                tone={
                  approvalSummary.badgeTone === "met" || approvalSummary.badgeTone === "approved"
                    ? "passed"
                    : approvalSummary.badgeTone === "needed"
                      ? "needed"
                      : "muted"
                }
              />
              <span>{approvalSummary.badgeLabel}</span>
            </span>
          ) : null}

          {showApprovals && checksSummary ? (
            <span aria-hidden="true" className="text-slate-400 dark:text-zinc-600">
              ·
            </span>
          ) : null}

          {checksSummary ? (
            <span
              className={cn(
                "inline-flex items-center gap-1.5 font-medium",
                checksSummary.tone === "passed" && "text-emerald-700 dark:text-emerald-300",
                checksSummary.tone === "running" && "text-amber-800 dark:text-amber-200",
                checksSummary.tone === "failed" && "text-rose-700 dark:text-rose-300",
                checksSummary.tone === "muted" && "text-slate-500 dark:text-zinc-400"
              )}
            >
              <StatusGlyph
                live={checksSummary.live}
                tone={checksSummary.tone === "running" ? "needed" : checksSummary.tone}
              />
              <span>{checksSummary.label}</span>
            </span>
          ) : null}
        </div>
      </summary>

      <div className="border-t border-slate-100 bg-slate-50/40 dark:border-zinc-800 dark:bg-zinc-950/30">
        {showApprovals ? (
          <ApprovalsCard
            embedded
            pathPrefix={pathPrefix}
            summary={approvalSummary}
            terminal={terminal}
          />
        ) : null}
        {showChecks ? (
          <div className={cn(showApprovals && "border-t border-slate-100 dark:border-zinc-800")}>
            <TriggersChecksPanel
              changesetId={changesetId}
              embedded
              patchsetId={patchsetId}
              signedOut={signedOut}
            />
          </div>
        ) : null}
      </div>
    </details>
  );
}

function StatusGlyph({
  live = false,
  tone
}: {
  live?: boolean;
  tone: "passed" | "needed" | "failed" | "muted";
}) {
  if (tone === "passed") {
    return (
      <svg
        aria-hidden="true"
        className="h-3.5 w-3.5 shrink-0"
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="2.5"
        viewBox="0 0 24 24"
      >
        <polyline points="20 6 9 17 4 12" />
      </svg>
    );
  }
  if (tone === "failed") {
    return (
      <span
        aria-hidden="true"
        className="inline-block h-2 w-2 shrink-0 rounded-full bg-rose-500"
      />
    );
  }
  if (tone === "needed") {
    return (
      <span
        aria-hidden="true"
        className={cn(
          "inline-block h-2 w-2 shrink-0 rounded-full bg-amber-500",
          live && "animate-pulse"
        )}
      />
    );
  }
  return (
    <span
      aria-hidden="true"
      className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-slate-400 dark:bg-zinc-500"
    />
  );
}
