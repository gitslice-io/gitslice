import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { RpcError, isDefiniteClientError, isUnauthenticatedError } from "../../api/rpcError";
import type { CheckRun, TriggerRun } from "../../api/types";
import { useApi } from "../../api/useApi";
import { Timestamp } from "../../components/Timestamp";
import { Button } from "../../components/ui/Button";
import { cn } from "../../lib/cn";
import {
  checkRunsKeyPrefix,
  checkRunsQuery,
  triggerRunsKey,
  triggerRunsQuery
} from "../../queries/checks";
import { groupTriggersAndChecks } from "./automationGrouping";
import { CheckRunItem } from "./CheckRunItem";
import { isTerminalCheckStatus } from "./checkStatus";
import { errorMessage } from "./status";
import {
  TriggerRunLog,
  TriggerStatusBadge,
  isFinalTriggerStatus
} from "./TriggerRunsPanel";

const POLL_MS = 3500;

interface TriggersChecksPanelProps {
  changesetId: string;
  patchsetId: string;
  signedOut?: boolean;
}

function SignInNotice() {
  return (
    <section className="mt-3 rounded-lg border border-dashed border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-3 md:px-5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          Triggers &amp; Checks
        </h2>
        <span className="text-xs text-slate-500 dark:text-zinc-400">Sign in to see checks</span>
      </div>
    </section>
  );
}

export function TriggersChecksPanel({
  changesetId,
  patchsetId,
  signedOut = false
}: TriggersChecksPanelProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [openCheckId, setOpenCheckId] = useState("");
  const [openTriggerLogId, setOpenTriggerLogId] = useState("");
  const [showSkipped, setShowSkipped] = useState(false);

  const checkQueryOpts = useMemo(
    () => checkRunsQuery(api, changesetId, patchsetId),
    [api, changesetId, patchsetId]
  );
  const triggersKey = useMemo(() => triggerRunsKey(changesetId), [changesetId]);

  const checksQuery = useQuery({
    ...checkQueryOpts,
    enabled: Boolean(changesetId && patchsetId) && !signedOut,
    retry: (failures, error) => !isUnauthenticatedError(error) && failures < 3,
    refetchInterval: (query) =>
      query.state.data?.some((run) => !isTerminalCheckStatus(run.status)) ? POLL_MS : false
  });

  const triggersQuery = useQuery({
    ...triggerRunsQuery(api, changesetId),
    enabled: Boolean(changesetId) && !signedOut,
    retry: (failures, error) =>
      !isDefiniteClientError(error) &&
      !isUnauthenticatedError(error) &&
      !(error instanceof RpcError && error.status === 503) &&
      failures < 3,
    refetchInterval: (query) =>
      query.state.data?.some((run) => !isFinalTriggerStatus(run.status)) ? POLL_MS : false
  });

  const refreshAll = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: checkQueryOpts.queryKey }),
      queryClient.invalidateQueries({ queryKey: checkRunsKeyPrefix(changesetId) }),
      queryClient.invalidateQueries({ queryKey: triggersKey })
    ]);

  const rerunCheckMutation = useMutation({
    mutationFn: (runId: string) => {
      const run = (checksQuery.data ?? []).find((r) => r.id === runId);
      return run?.triggerRunId && run.provenance === "trigger"
        ? api.rerunTriggerRun({ runId: run.triggerRunId })
        : api.rerunCheck({ runId });
    },
    onSuccess: refreshAll
  });

  const rerunTriggerMutation = useMutation({
    mutationFn: (runId: string) => api.rerunTriggerRun({ runId }),
    onSuccess: refreshAll
  });

  const cancelTriggerMutation = useMutation({
    mutationFn: (runId: string) => api.cancelTriggerRun({ runId }),
    onSuccess: refreshAll
  });

  const checkRuns = checksQuery.data ?? [];
  const triggerRuns = triggersQuery.isError ? [] : (triggersQuery.data ?? []);

  const grouped = useMemo(
    () => groupTriggersAndChecks(triggerRuns, checkRuns, patchsetId),
    [triggerRuns, checkRuns, patchsetId]
  );

  useEffect(() => {
    setOpenCheckId("");
    setOpenTriggerLogId("");
    setShowSkipped(false);
  }, [changesetId, patchsetId]);

  useEffect(() => {
    if (openCheckId && !checkRuns.some((run) => run.id === openCheckId)) {
      setOpenCheckId("");
    }
  }, [checkRuns, openCheckId]);

  if (!changesetId || !patchsetId) {
    return null;
  }

  if (signedOut) {
    return <SignInNotice />;
  }

  if (checksQuery.isError && isUnauthenticatedError(checksQuery.error)) {
    return <SignInNotice />;
  }

  const totalTriggers = grouped.primaryTriggers.length + grouped.skippedTriggers.length;
  const totalChecks = checkRuns.length;

  if (
    checksQuery.isPending &&
    (triggersQuery.isPending || triggersQuery.isError) &&
    totalTriggers === 0 &&
    totalChecks === 0
  ) {
    return null;
  }

  if (checksQuery.isError && totalTriggers === 0) {
    return (
      <section className="mt-3 rounded-lg border border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 px-3 py-3 text-sm text-rose-900 dark:text-rose-200 md:px-5">
        <h2 className="font-semibold">Checks</h2>
        <p className="mt-1">{errorMessage(checksQuery.error)}</p>
      </section>
    );
  }

  if (totalTriggers === 0 && totalChecks === 0) {
    return (
      <section className="mt-3 rounded-lg border border-dashed border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-3 md:px-5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            Checks
          </h2>
          <span className="text-xs text-slate-500 dark:text-zinc-400">No checks</span>
        </div>
      </section>
    );
  }

  const hasLive =
    checkRuns.some((run) => !isTerminalCheckStatus(run.status)) ||
    grouped.primaryTriggers.some(({ trigger }) => !isFinalTriggerStatus(trigger.status));

  const actionError =
    rerunCheckMutation.error ?? rerunTriggerMutation.error ?? cancelTriggerMutation.error;
  const hasTriggers = totalTriggers > 0;
  const heading = hasTriggers ? "Triggers" : "Checks";

  const summaryParts: string[] = [];
  if (grouped.primaryTriggers.length > 0) {
    summaryParts.push(
      `${grouped.primaryTriggers.length} trigger${grouped.primaryTriggers.length === 1 ? "" : "s"}`
    );
  }
  if (totalChecks > 0) {
    summaryParts.push(`${totalChecks} check${totalChecks === 1 ? "" : "s"}`);
  }

  const toggleCheck = (runId: string) =>
    setOpenCheckId((current) => (current === runId ? "" : runId));
  const toggleTriggerLog = (runId: string) =>
    setOpenTriggerLogId((current) => (current === runId ? "" : runId));

  return (
    <section className="mt-3 rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 dark:border-zinc-800 px-3 py-2.5 md:px-5">
        <div className="min-w-0">
          <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            {heading}
          </h2>
          <p className="mt-0.5 truncate text-xs text-slate-500 dark:text-zinc-400">
            Patchset {patchsetId}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {summaryParts.length > 0 ? (
            <span className="text-xs text-slate-500 dark:text-zinc-400">
              {summaryParts.join(" · ")}
            </span>
          ) : null}
          {hasLive ? (
            <span className="inline-flex items-center gap-1.5 rounded-md border border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 px-2 py-1 text-xs font-semibold text-amber-900 dark:text-amber-200">
              <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500" />
              Live
            </span>
          ) : null}
        </div>
      </div>

      {actionError ? (
        <p
          className="border-b border-slate-100 dark:border-zinc-800 px-3 py-2 text-sm text-rose-700 dark:text-rose-300 md:px-5"
          role="alert"
        >
          {errorMessage(actionError)}
        </p>
      ) : null}

      {grouped.primaryTriggers.length > 0 ? (
        <ul className="divide-y divide-slate-100 dark:divide-zinc-800">
          {grouped.primaryTriggers.map(({ trigger, checks }) => (
            <TriggerGroupRow
              api={api}
              cancelPending={cancelTriggerMutation.isPending}
              checks={checks}
              key={trigger.id ?? trigger.triggerId}
              onCancelTrigger={(id) => cancelTriggerMutation.mutate(id)}
              onRerunCheck={(id) => rerunCheckMutation.mutate(id)}
              onRerunTrigger={(id) => rerunTriggerMutation.mutate(id)}
              onToggleCheck={toggleCheck}
              onToggleTriggerLog={toggleTriggerLog}
              openCheckId={openCheckId}
              openTriggerLogId={openTriggerLogId}
              rerunCheckPendingId={
                rerunCheckMutation.isPending ? rerunCheckMutation.variables ?? "" : ""
              }
              rerunTriggerPending={rerunTriggerMutation.isPending}
              trigger={trigger}
            />
          ))}
        </ul>
      ) : null}

      {grouped.standaloneChecks.length > 0 ? (
        <div className={cn(grouped.primaryTriggers.length > 0 && "border-t border-slate-100 dark:border-zinc-800")}>
          {hasTriggers ? (
            <div className="border-b border-slate-100 dark:border-zinc-800 bg-slate-50/60 dark:bg-zinc-950/50 px-3 py-1.5 md:px-5">
              <h3 className="text-[11px] font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
                Other checks
              </h3>
            </div>
          ) : null}
          <div className="divide-y divide-slate-100 dark:divide-zinc-800">
            {grouped.standaloneChecks.map((run) => {
              const runId = run.id ?? "";
              return (
                <CheckRunItem
                  api={api}
                  key={runId || run.checkName || run.status}
                  onRerun={(id) => rerunCheckMutation.mutate(id)}
                  onToggle={toggleCheck}
                  rerunning={rerunCheckMutation.isPending && rerunCheckMutation.variables === runId}
                  run={run}
                  selected={openCheckId === runId}
                />
              );
            })}
          </div>
        </div>
      ) : null}

      {grouped.skippedTriggers.length > 0 ? (
        <div className="border-t border-slate-100 dark:border-zinc-800 bg-slate-50/60 dark:bg-zinc-950/40">
          <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 md:px-5">
            <span className="text-xs text-slate-500 dark:text-zinc-400">
              {grouped.skippedTriggers.length} skipped trigger
              {grouped.skippedTriggers.length === 1 ? "" : "s"} (
              <span className="font-mono">
                {grouped.skippedTriggers.map((t) => t.triggerId).filter(Boolean).join(", ")}
              </span>
              ) — no matching checks
            </span>
            <button
              aria-expanded={showSkipped}
              className="text-xs font-medium text-slate-600 underline-offset-2 hover:text-zinc-950 hover:underline dark:text-zinc-400 dark:hover:text-zinc-100"
              onClick={() => setShowSkipped((prev) => !prev)}
              type="button"
            >
              {showSkipped ? "Hide" : "Show"}
            </button>
          </div>
          {showSkipped ? (
            <ul className="divide-y divide-slate-100 border-t border-slate-100 dark:divide-zinc-800 dark:border-zinc-800">
              {grouped.skippedTriggers.map((trigger) => (
                <TriggerGroupRow
                  api={api}
                  cancelPending={cancelTriggerMutation.isPending}
                  checks={[]}
                  key={trigger.id ?? trigger.triggerId}
                  onCancelTrigger={(id) => cancelTriggerMutation.mutate(id)}
                  onRerunCheck={(id) => rerunCheckMutation.mutate(id)}
                  onRerunTrigger={(id) => rerunTriggerMutation.mutate(id)}
                  onToggleCheck={toggleCheck}
                  onToggleTriggerLog={toggleTriggerLog}
                  openCheckId={openCheckId}
                  openTriggerLogId={openTriggerLogId}
                  rerunCheckPendingId=""
                  rerunTriggerPending={rerunTriggerMutation.isPending}
                  trigger={trigger}
                />
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

interface TriggerGroupRowProps {
  api: ReturnType<typeof useApi>;
  cancelPending: boolean;
  checks: CheckRun[];
  onCancelTrigger: (runId: string) => void;
  onRerunCheck: (runId: string) => void;
  onRerunTrigger: (runId: string) => void;
  onToggleCheck: (runId: string) => void;
  onToggleTriggerLog: (runId: string) => void;
  openCheckId: string;
  openTriggerLogId: string;
  rerunCheckPendingId: string;
  rerunTriggerPending: boolean;
  trigger: TriggerRun;
}

function TriggerGroupRow({
  api,
  cancelPending,
  checks,
  onCancelTrigger,
  onRerunCheck,
  onRerunTrigger,
  onToggleCheck,
  onToggleTriggerLog,
  openCheckId,
  openTriggerLogId,
  rerunCheckPendingId,
  rerunTriggerPending,
  trigger
}: TriggerGroupRowProps) {
  const id = trigger.id ?? "";
  const logOpen = openTriggerLogId === id;
  const final = isFinalTriggerStatus(trigger.status);

  return (
    <li className={cn(logOpen && "bg-slate-50/70 dark:bg-zinc-950/60")}>
      <div className="grid grid-cols-1 gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto] md:px-5">
        <button
          aria-expanded={logOpen}
          className="min-w-0 text-left"
          onClick={() => onToggleTriggerLog(id)}
          type="button"
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">
              {trigger.triggerId}
            </span>
            <TriggerStatusBadge status={trigger.status} />
            <span className="text-xs text-slate-500 dark:text-zinc-400">
              {trigger.eventKind} · {trigger.action} · <Timestamp relative value={trigger.createdAt} />
            </span>
          </div>
          {trigger.error ? (
            <p className="mt-1 line-clamp-2 text-sm text-rose-700 dark:text-rose-300">
              {trigger.error}
            </p>
          ) : null}
        </button>

        <div className="flex items-center gap-2 md:justify-end">
          <button
            className="text-xs font-medium text-slate-500 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100"
            onClick={() => onToggleTriggerLog(id)}
            type="button"
          >
            {logOpen ? "Hide log" : checks.length > 0 ? "Trigger log" : "View log"}
          </button>
          {final && trigger.action ? (
            <Button
              disabled={rerunTriggerPending}
              onClick={() => onRerunTrigger(id)}
              size="xs"
            >
              Rerun
            </Button>
          ) : null}
          {!final ? (
            <Button
              disabled={cancelPending}
              onClick={() => onCancelTrigger(id)}
              size="xs"
            >
              Cancel
            </Button>
          ) : null}
        </div>
      </div>

      {logOpen ? <TriggerRunLog run={trigger} /> : null}

      {checks.length > 0 ? (
        <div className="px-3 pb-3 md:px-5">
          <div className="space-y-2 border-l-2 border-slate-200 dark:border-zinc-800 pl-3">
            {checks.map((check) => {
              const checkId = check.id ?? "";
              return (
                <CheckRunItem
                  api={api}
                  key={checkId || check.checkName || check.status}
                  nested
                  onRerun={onRerunCheck}
                  onToggle={onToggleCheck}
                  rerunning={rerunCheckPendingId === checkId}
                  run={check}
                  selected={openCheckId === checkId}
                />
              );
            })}
          </div>
        </div>
      ) : null}
    </li>
  );
}
