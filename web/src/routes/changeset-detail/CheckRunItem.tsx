import type { CheckRun } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { formatRelativeTime } from "../../lib/time";
import { CheckRunLogTail, TriggerCheckDetails } from "./CheckRunLogTail";
import {
  checkStatusClass,
  checkStatusDotClass,
  humanizeCheckStatus,
  isTerminalCheckStatus,
  normalizeCheckStatus,
  normalizeProvenance,
  safeDetailsUrl,
  shouldShowExitCode
} from "./checkStatus";

interface CheckRunItemProps {
  api: ApiClient;
  nested?: boolean;
  onRerun: (runId: string) => void;
  onToggle: (runId: string) => void;
  rerunning: boolean;
  run: CheckRun;
  selected: boolean;
}

export function CheckRunItem({
  api,
  nested = false,
  onRerun,
  onToggle,
  rerunning,
  run,
  selected
}: CheckRunItemProps) {
  const runId = run.id ?? "";
  const canRerun = Boolean(runId && isTerminalCheckStatus(run.status));
  const offlineDaemonMessage = queuedOfflineDaemonMessage(run);
  const detailsHref = safeDetailsUrl(run.detailsUrl);

  return (
    <div
      className={cn(
        nested
          ? "rounded-md border border-slate-200/80 dark:border-zinc-800 bg-slate-50/70 dark:bg-zinc-950/60"
          : undefined,
        selected && !nested && "bg-slate-50 dark:bg-zinc-950"
      )}
    >
      <div
        className={cn(
          "grid grid-cols-1 gap-2 transition md:grid-cols-[minmax(0,1fr)_auto]",
          nested ? "px-3 py-2.5" : "px-3 py-3 md:px-5"
        )}
      >
        <button
          aria-expanded={selected}
          className="min-w-0 text-left transition hover:text-zinc-950 dark:hover:text-zinc-50 active:scale-[0.995]"
          disabled={!runId}
          onClick={() => onToggle(runId)}
          type="button"
        >
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                {run.checkName || "Unnamed check"}
              </span>
              <CheckStatusBadge status={run.status} />
              <ProvenanceTag provenance={run.provenance} />
              {run.daemonName ? (
                <span
                  className="truncate text-xs text-slate-500 dark:text-zinc-400"
                  title={`ran on ${run.daemonName}`}
                >
                  on {run.daemonName}
                </span>
              ) : null}
              {shouldShowExitCode(run) ? (
                <span className="rounded bg-slate-100 dark:bg-zinc-800 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
                  exit {run.exitCode}
                </span>
              ) : null}
            </div>
            {run.summary ? (
              <p className="mt-1 line-clamp-2 text-sm leading-5 text-slate-600 dark:text-zinc-400">
                {run.summary}
              </p>
            ) : null}
            {offlineDaemonMessage ? (
              <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {offlineDaemonMessage}
              </p>
            ) : null}
          </div>
        </button>
        <div className="flex items-center justify-start gap-2 md:justify-end">
          {detailsHref ? (
            <a
              className="text-xs font-semibold text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
              href={detailsHref}
              rel="noopener noreferrer"
              target="_blank"
            >
              Details
            </a>
          ) : null}
          <button
            className="text-xs font-medium text-slate-500 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            disabled={!runId}
            onClick={() => onToggle(runId)}
            type="button"
          >
            {selected ? "Logs open" : "View logs"}
          </button>
          {canRerun ? (
            <button
              className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2 py-1 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 disabled:cursor-not-allowed disabled:opacity-60"
              disabled={rerunning}
              onClick={() => onRerun(runId)}
              type="button"
            >
              {rerunning ? "Rerunning" : "Rerun"}
            </button>
          ) : null}
        </div>
      </div>
      {selected ? (
        run.automationRunId && run.provenance === "trigger" ? (
          <TriggerCheckDetails run={run} />
        ) : (
          <CheckRunLogTail api={api} run={run} />
        )
      ) : null}
    </div>
  );
}

export function CheckStatusBadge({ status }: { status?: string }) {
  const normalized = normalizeCheckStatus(status);
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-[11px] font-semibold md:py-1 md:text-xs",
        checkStatusClass(normalized)
      )}
      title={normalized}
    >
      <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", checkStatusDotClass(normalized))} />
      {humanizeCheckStatus(normalized)}
    </span>
  );
}

export function ProvenanceTag({ provenance }: { provenance?: string }) {
  const normalized = normalizeProvenance(provenance);
  return (
    <span
      className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-normal text-slate-600 dark:text-zinc-400 md:py-1 md:text-xs"
      title={`provenance: ${normalized}`}
    >
      {normalized}
    </span>
  );
}

export function queuedOfflineDaemonMessage(run: CheckRun) {
  if (
    normalizeCheckStatus(run.status) !== "queued" ||
    !run.daemonId ||
    run.daemonOnline ||
    !run.daemonLastSeenAt
  ) {
    return "";
  }
  const daemon = run.daemonName || run.daemonId;
  return `Waiting for ${daemon}, offline, last seen ${formatRelativeTime(run.daemonLastSeenAt)}`;
}
