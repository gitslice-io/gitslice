import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { RpcError } from "../../api/client";
import type { TriggerRun } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { formatRelativeTime } from "../../lib/time";
import { errorMessage } from "./status";

// Trigger runs (design/34_triggers.md) about this changeset: the triggers in
// the folders it touches that fired for its events, with their logs.

const POLL_MS = 3500;
const finalStatuses = new Set(["succeeded", "failed", "errored", "canceled", "timed_out"]);

export function isFinalTriggerStatus(status?: string) {
  return finalStatuses.has((status ?? "").toLowerCase());
}

export function TriggerRunsPanel({ changesetId, patchsetId }: { changesetId: string; patchsetId: string }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [openRunId, setOpenRunId] = useState("");
  const queryKey = ["triggerRuns", changesetId];
  const runsQuery = useQuery({
    enabled: Boolean(changesetId),
    queryKey,
    queryFn: async () => (await api.listTriggerRuns({ changesetId, limit: 50 })).runs ?? [],
    retry: (failures, error) => !(error instanceof RpcError && (error.status === 401 || error.status === 503)) && failures < 3,
    refetchInterval: (query) => (query.state.data?.some((run) => !isFinalTriggerStatus(run.status)) ? POLL_MS : false)
  });
  const refresh = () => Promise.all([
    queryClient.invalidateQueries({ queryKey }),
    queryClient.invalidateQueries({ queryKey: ["checkRuns", changesetId] })
  ]);
  const rerun = useMutation({ mutationFn: (runId: string) => api.rerunTriggerRun({ runId }), onSuccess: refresh });
  const cancel = useMutation({ mutationFn: (runId: string) => api.cancelTriggerRun({ runId }), onSuccess: refresh });

  // A patchset's runs, and runs about the changeset as a whole (a landing).
  const runs = (runsQuery.data ?? []).filter((run) => !run.patchsetId || run.patchsetId === patchsetId);
  if (!changesetId || runsQuery.isError || runs.length === 0) {
    return null;
  }
  const actionError = rerun.error ?? cancel.error;

  return (
    <section className="mt-3 rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 shadow-sm shadow-slate-200/50 dark:shadow-black/50">
      <div className="flex items-center justify-between gap-2 border-b border-slate-100 dark:border-zinc-800 px-3 py-2.5 md:px-5">
        <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">Triggers</h2>
        <span className="text-xs text-slate-500 dark:text-zinc-400">{runs.length} run{runs.length === 1 ? "" : "s"}</span>
      </div>
      {actionError ? (
        <p className="border-b border-slate-100 dark:border-zinc-800 px-3 py-2 text-sm text-rose-700 dark:text-rose-300 md:px-5" role="alert">
          {errorMessage(actionError)}
        </p>
      ) : null}
      <ul className="divide-y divide-slate-100 dark:divide-zinc-800">
        {runs.map((run) => {
          const id = run.id ?? "";
          const open = openRunId === id;
          const final = isFinalTriggerStatus(run.status);
          return (
            <li key={id} className={cn(open && "bg-slate-50 dark:bg-zinc-950")}>
              <div className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto] md:px-5">
                <button
                  aria-expanded={open}
                  className="min-w-0 text-left"
                  onClick={() => setOpenRunId(open ? "" : id)}
                  type="button"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">{run.triggerId}</span>
                    <TriggerStatusBadge status={run.status} />
                    <span className="text-xs text-slate-500 dark:text-zinc-400">
                      {run.eventKind} · {run.action} · {formatRelativeTime(run.createdAt)}
                    </span>
                  </div>
                  {run.error ? <p className="mt-1 line-clamp-2 text-sm text-slate-600 dark:text-zinc-400">{run.error}</p> : null}
                </button>
                <div className="flex items-center gap-2 md:justify-end">
                  <span className="text-xs font-medium text-slate-500 dark:text-zinc-400">{open ? "Log open" : "View log"}</span>
                  {final && run.action ? (
                    <button
                      className={actionButton}
                      disabled={rerun.isPending}
                      onClick={() => rerun.mutate(id)}
                      type="button"
                    >
                      Rerun
                    </button>
                  ) : null}
                  {!final ? (
                    <button
                      className={actionButton}
                      disabled={cancel.isPending}
                      onClick={() => cancel.mutate(id)}
                      type="button"
                    >
                      Cancel
                    </button>
                  ) : null}
                </div>
              </div>
              {open ? <TriggerRunLog run={run} /> : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

const actionButton =
  "rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2 py-1 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 disabled:opacity-60";

// TriggerRunLog shows a run's log, polling while the run is open.
export function TriggerRunLog({ run }: { run: TriggerRun }) {
  const api = useApi();
  const runId = run.id ?? "";
  const logsQuery = useQuery({
    enabled: Boolean(runId),
    queryKey: ["triggerRunLogs", runId],
    queryFn: async () => (await api.listTriggerRunLogs({ runId, afterSeq: "0" })).logs ?? [],
    refetchInterval: isFinalTriggerStatus(run.status) ? false : POLL_MS
  });
  const text = (logsQuery.data ?? []).map((log) => log.chunk ?? "").join("");
  return (
    <div className="border-t border-slate-100 dark:border-zinc-800 bg-slate-950 px-3 py-3 md:px-5">
      {logsQuery.isError ? (
        <p className="text-sm text-rose-200">{errorMessage(logsQuery.error)}</p>
      ) : text ? (
        <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-slate-100">
          {text}
        </pre>
      ) : (
        <p className="rounded-md bg-zinc-950 p-3 font-mono text-xs text-slate-400">
          {logsQuery.isPending ? "Loading log..." : "No log output."}
        </p>
      )}
    </div>
  );
}

function TriggerStatusBadge({ status }: { status?: string }) {
  const s = (status ?? "queued").toLowerCase();
  const tone =
    s === "succeeded"
      ? "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-800 dark:text-emerald-200"
      : s === "failed" || s === "errored" || s === "timed_out"
        ? "border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 text-rose-800 dark:text-rose-200"
        : s === "running" || s === "waiting"
          ? "border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 text-amber-900 dark:text-amber-200"
          : "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-700 dark:text-zinc-300";
  const label = s === "timed_out" ? "Timed out" : s.charAt(0).toUpperCase() + s.slice(1);
  return (
    <span className={cn("inline-flex items-center rounded-md border px-2 py-0.5 text-[11px] font-semibold md:text-xs", tone)}>
      {label}
    </span>
  );
}
