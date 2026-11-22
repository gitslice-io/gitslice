import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import type { AutomationRun } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { getErrorMessage } from "../../lib/errors";
import { triggerRunLogsQuery } from "../../queries/checks";

const POLL_MS = 3500;
const finalStatuses = new Set([
  "succeeded",
  "failed",
  "errored",
  "canceled",
  "timed_out",
]);

export function isFinalTriggerStatus(status?: string) {
  return finalStatuses.has((status ?? "").toLowerCase());
}

// AutomationRunLog shows a run's log (and child pipeline steps for action === "steps"),
// polling while the run is open.
export function AutomationRunLog({ run }: { run: AutomationRun }) {
  const api = useApi();
  const runId = run.id ?? "";
  const isPipeline = run.action === "steps";
  const [openStepId, setOpenStepId] = useState("");
  const logsQuery = useQuery({
    ...triggerRunLogsQuery(api, runId),
    enabled: Boolean(runId),
    refetchInterval: isFinalTriggerStatus(run.status) ? false : POLL_MS,
  });
  const stepsQuery = useQuery({
    queryKey: ["automationRunSteps", runId],
    queryFn: async () =>
      (await api.listAutomationRuns({ parentRunId: runId, limit: 200 })).runs ??
      [],
    enabled: Boolean(runId && isPipeline),
    refetchInterval: isFinalTriggerStatus(run.status) ? false : POLL_MS,
  });
  const text = (logsQuery.data ?? []).map((log) => log.chunk ?? "").join("");
  const outputs = Object.entries(run.outputs ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );
  const steps = stepsQuery.data ?? [];

  return (
    <div className="border-t border-slate-100 dark:border-zinc-800 bg-slate-950 px-3 py-3 md:px-5">
      {outputs.length > 0 ? (
        <div className="mb-2 flex flex-wrap items-center gap-1.5">
          <span className="text-[11px] font-semibold uppercase tracking-wide text-slate-400">
            Outputs
          </span>
          {outputs.map(([k, v]) => (
            <span
              key={k}
              className="rounded border border-zinc-700 bg-zinc-900 px-1.5 py-0.5 font-mono text-[11px] text-slate-200"
            >
              {k}={v}
            </span>
          ))}
        </div>
      ) : null}
      {isPipeline ? (
        <div className="mb-2 rounded-md border border-zinc-800 bg-zinc-900/70">
          <div className="border-b border-zinc-800 px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wide text-slate-400">
            Pipeline steps ({steps.length})
          </div>
          {stepsQuery.isPending ? (
            <p className="px-3 py-2 font-mono text-xs text-slate-400">
              Loading steps...
            </p>
          ) : steps.length === 0 ? (
            <p className="px-3 py-2 font-mono text-xs text-slate-400">
              No steps started yet.
            </p>
          ) : (
            <ul className="divide-y divide-zinc-800">
              {steps.map((step, idx) => {
                const sid = step.id ?? "";
                const expanded = openStepId === sid;
                const stepNum =
                  (step.stepIndex ?? 0) > 0 ? (step.stepIndex as number) : idx + 1;
                const label = step.stepId || `step_${stepNum}`;
                const stepOutputs = Object.entries(step.outputs ?? {}).sort(
                  ([a], [b]) => a.localeCompare(b),
                );
                return (
                  <li key={sid}>
                    <button
                      aria-expanded={expanded}
                      className="flex w-full flex-wrap items-center justify-between gap-2 px-3 py-2 text-left text-xs hover:bg-zinc-900"
                      onClick={() => setOpenStepId(expanded ? "" : sid)}
                      type="button"
                    >
                      <span className="flex flex-wrap items-center gap-2">
                        <span className="font-mono text-slate-400">
                          {stepNum}.
                        </span>
                        <span className="font-mono font-semibold text-slate-100">
                          {label}
                        </span>
                        <TriggerStatusBadge status={step.status} />
                        <span className="text-slate-400">{step.action}</span>
                        {step.templateId ? (
                          <span className="font-mono text-[11px] text-indigo-300">
                            uses {step.templateId}
                          </span>
                        ) : null}
                      </span>
                      <span className="text-[11px] text-slate-400">
                        {expanded ? "Hide log" : "View log"}
                      </span>
                    </button>
                    {stepOutputs.length > 0 ? (
                      <div className="flex flex-wrap gap-1.5 px-3 pb-2">
                        {stepOutputs.map(([k, v]) => (
                          <span
                            key={k}
                            className="rounded border border-zinc-700 bg-zinc-950 px-1.5 py-px font-mono text-[11px] text-slate-300"
                          >
                            {k}={v}
                          </span>
                        ))}
                      </div>
                    ) : null}
                    {step.error ? (
                      <p className="px-3 pb-2 font-mono text-xs text-rose-300">
                        {step.error}
                      </p>
                    ) : null}
                    {expanded ? <AutomationRunLog run={step} /> : null}
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      ) : null}
      {logsQuery.isError ? (
        <p className="text-sm text-rose-200">{getErrorMessage(logsQuery.error, "Request failed.")}</p>
      ) : text ? (
        <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-slate-100">
          {text}
        </pre>
      ) : isPipeline ? null : (
        <p className="rounded-md bg-zinc-950 p-3 font-mono text-xs text-slate-400">
          {logsQuery.isPending ? "Loading log..." : "No log output."}
        </p>
      )}
    </div>
  );
}

export function TriggerStatusBadge({ status }: { status?: string }) {
  const s = (status ?? "queued").toLowerCase();
  const tone =
    s === "succeeded"
      ? "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-800 dark:text-emerald-200"
      : s === "failed" || s === "errored" || s === "timed_out"
        ? "border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 text-rose-800 dark:text-rose-200"
        : s === "running" || s === "waiting"
          ? "border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 text-amber-900 dark:text-amber-200"
          : "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-700 dark:text-zinc-300";
  const label =
    s === "timed_out" ? "Timed out" : s.charAt(0).toUpperCase() + s.slice(1);
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-md border px-2 py-0.5 text-[11px] font-semibold md:text-xs",
        tone,
      )}
    >
      {label}
    </span>
  );
}
