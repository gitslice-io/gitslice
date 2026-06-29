import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState
} from "react";
import { useQuery } from "@tanstack/react-query";

import type { CheckRun, CheckRunLog } from "../../api/types";
import { useApi, type ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { errorMessage } from "./status";

const CHECK_POLL_INTERVAL_MS = 3500;
const terminalCheckStatuses = new Set([
  "passed",
  "failed",
  "errored",
  "skipped",
  "canceled"
]);

interface ChecksPanelProps {
  changesetId: string;
  patchsetId: string;
}

export function ChecksPanel({ changesetId, patchsetId }: ChecksPanelProps) {
  const api = useApi();
  const [selectedRunId, setSelectedRunId] = useState("");

  const runsQuery = useQuery({
    enabled: Boolean(changesetId && patchsetId),
    queryKey: ["checkRuns", changesetId, patchsetId],
    queryFn: async () =>
      (await api.listCheckRuns({ changesetId, patchsetId })).runs ?? [],
    refetchInterval: (query) =>
      query.state.data?.some((run) => !isTerminalCheckStatus(run.status))
        ? CHECK_POLL_INTERVAL_MS
        : false
  });

  const runs = useMemo(
    () => sortCheckRuns(runsQuery.data ?? []),
    [runsQuery.data]
  );
  const selectedRun = runs.find((run) => run.id === selectedRunId);
  const hasNonTerminalRun = runs.some(
    (run) => !isTerminalCheckStatus(run.status)
  );

  useEffect(() => {
    setSelectedRunId("");
  }, [changesetId, patchsetId]);

  useEffect(() => {
    if (selectedRunId && !runs.some((run) => run.id === selectedRunId)) {
      setSelectedRunId("");
    }
  }, [runs, selectedRunId]);

  if (!changesetId || !patchsetId) {
    return null;
  }

  if (runsQuery.isPending && runs.length === 0) {
    return null;
  }

  if (runsQuery.isError) {
    return (
      <section className="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-3 text-sm text-rose-900 md:px-5">
        <h2 className="font-semibold">Checks</h2>
        <p className="mt-1">{errorMessage(runsQuery.error)}</p>
      </section>
    );
  }

  if (runs.length === 0) {
    return (
      <section className="mt-3 rounded-lg border border-dashed border-slate-200 bg-white px-3 py-3 md:px-5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            Checks
          </h2>
          <span className="text-xs text-slate-500">No checks</span>
        </div>
      </section>
    );
  }

  return (
    <section className="mt-3 rounded-lg border border-slate-200 bg-white shadow-sm shadow-slate-200/50">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-100 px-3 py-2.5 md:px-5">
        <div className="min-w-0">
          <h2 className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            Checks
          </h2>
          <p className="mt-0.5 truncate text-xs text-slate-500">
            Patchset {patchsetId}
          </p>
        </div>
        {hasNonTerminalRun ? (
          <span className="inline-flex items-center gap-1.5 rounded-md border border-amber-200 bg-amber-50 px-2 py-1 text-xs font-semibold text-amber-900">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500" />
            Live
          </span>
        ) : null}
      </div>

      <div className="divide-y divide-slate-100">
        {runs.map((run) => {
          const runId = run.id ?? "";
          const selected = selectedRunId === runId;
          return (
            <button
              aria-expanded={selected}
              className={cn(
                "grid w-full gap-2 px-3 py-3 text-left transition hover:bg-slate-50 active:scale-[0.995] md:grid-cols-[minmax(0,1fr)_auto] md:px-5",
                selected && "bg-slate-50"
              )}
              disabled={!runId}
              key={runId || run.checkName || run.status}
              onClick={() => setSelectedRunId(runId)}
              type="button"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-semibold text-zinc-950">
                    {run.checkName || "Unnamed check"}
                  </span>
                  <CheckStatusBadge status={run.status} />
                  <ProvenanceTag provenance={run.provenance} />
                  {shouldShowExitCode(run) ? (
                    <span className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs text-slate-700">
                      exit {run.exitCode}
                    </span>
                  ) : null}
                </div>
                {run.summary ? (
                  <p className="mt-1 line-clamp-2 text-sm leading-5 text-slate-600">
                    {run.summary}
                  </p>
                ) : null}
              </div>
              <span className="self-center text-xs font-medium text-slate-500">
                {selected ? "Logs open" : "View logs"}
              </span>
            </button>
          );
        })}
      </div>

      {selectedRun ? (
        <CheckRunLogTail api={api} run={selectedRun} />
      ) : null}
    </section>
  );
}

function CheckRunLogTail({
  api,
  run
}: {
  api: ApiClient;
  run: CheckRun;
}) {
  const [logs, setLogs] = useState<CheckRunLog[]>([]);
  const [streamError, setStreamError] = useState("");
  const [streamClosed, setStreamClosed] = useState(false);
  const preRef = useRef<HTMLPreElement | null>(null);
  const runId = run.id ?? "";
  const logText = logs.map((log) => log.chunk ?? "").join("");
  const live = !isTerminalCheckStatus(run.status) && !streamClosed;

  useEffect(() => {
    if (!runId) {
      return;
    }

    const controller = new AbortController();
    setLogs([]);
    setStreamError("");
    setStreamClosed(false);

    async function readStream() {
      try {
        for await (const log of api.streamCheckRun(
          { runId, afterSeq: 0 },
          controller.signal
        )) {
          if (controller.signal.aborted) {
            return;
          }
          setLogs((current) => appendCheckLog(current, log));
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setStreamError(errorMessage(error));
        }
      } finally {
        if (!controller.signal.aborted) {
          setStreamClosed(true);
        }
      }
    }

    void readStream();

    return () => {
      controller.abort();
    };
  }, [api, runId]);

  useLayoutEffect(() => {
    const node = preRef.current;
    if (node) {
      node.scrollTop = node.scrollHeight;
    }
  }, [logText]);

  return (
    <div className="border-t border-slate-100 bg-slate-950 px-3 py-3 md:px-5">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-semibold",
              live
                ? "bg-emerald-400/10 text-emerald-200"
                : "bg-white/10 text-slate-200"
            )}
          >
            <span
              className={cn(
                "h-1.5 w-1.5 rounded-full",
                live ? "animate-pulse bg-emerald-300" : "bg-slate-400"
              )}
            />
            {live ? "Live tail" : "Log replay"}
          </span>
          <span className="truncate font-mono text-xs text-slate-400">
            {run.checkName || run.id}
          </span>
        </div>
        <span className="font-mono text-xs text-slate-500">
          {logs.length ? `${logs.length} chunks` : "after_seq=0"}
        </span>
      </div>

      {streamError ? (
        <div className="mb-2 rounded-md border border-rose-300/30 bg-rose-400/10 px-3 py-2 text-sm text-rose-100">
          {streamError}
        </div>
      ) : null}

      {logText ? (
        <pre
          className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-slate-100"
          ref={preRef}
        >
          {logText}
        </pre>
      ) : (
        <div className="rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-slate-400">
          {streamClosed
            ? "No log output returned for this run."
            : "Waiting for log output..."}
        </div>
      )}
    </div>
  );
}

function CheckStatusBadge({ status }: { status?: string }) {
  const normalized = normalizeCheckStatus(status);
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-[11px] font-semibold md:py-1 md:text-xs",
        checkStatusClass(normalized)
      )}
      title={normalized}
    >
      <span
        aria-hidden
        className={cn("h-1.5 w-1.5 rounded-full", checkStatusDotClass(normalized))}
      />
      {humanizeCheckStatus(normalized)}
    </span>
  );
}

function ProvenanceTag({ provenance }: { provenance?: string }) {
  const normalized = normalizeProvenance(provenance);
  return (
    <span
      className="rounded-md border border-slate-200 bg-white px-2 py-0.5 text-[11px] font-semibold uppercase tracking-normal text-slate-600 md:py-1 md:text-xs"
      title={`provenance: ${normalized}`}
    >
      {normalized}
    </span>
  );
}

function appendCheckLog(current: CheckRunLog[], nextLog: CheckRunLog) {
  const nextSeq = Number(nextLog.seq ?? 0);
  if (Number.isFinite(nextSeq) && current.some((log) => Number(log.seq ?? 0) === nextSeq)) {
    return current;
  }
  return [...current, nextLog];
}

function sortCheckRuns(runs: CheckRun[]) {
  return [...runs].sort((left, right) => {
    const leftName = left.checkName ?? "";
    const rightName = right.checkName ?? "";
    if (leftName !== rightName) {
      return leftName.localeCompare(rightName);
    }
    return (left.id ?? "").localeCompare(right.id ?? "");
  });
}

function shouldShowExitCode(run: CheckRun) {
  const status = normalizeCheckStatus(run.status);
  return (
    typeof run.exitCode === "number" &&
    (status === "passed" || status === "failed" || status === "errored")
  );
}

function isTerminalCheckStatus(status?: string) {
  return terminalCheckStatuses.has(normalizeCheckStatus(status));
}

function normalizeCheckStatus(status?: string) {
  return (status || "queued").toLowerCase();
}

function normalizeProvenance(provenance?: string) {
  const normalized = (provenance || "").toLowerCase();
  if (normalized === "self" || normalized === "ci") {
    return normalized;
  }
  return normalized || "unknown";
}

function humanizeCheckStatus(status: string) {
  return status.charAt(0).toUpperCase() + status.slice(1);
}

function checkStatusClass(status: string) {
  switch (status) {
    case "passed":
      return "border-emerald-200 bg-emerald-50 text-emerald-800";
    case "failed":
    case "errored":
      return "border-rose-200 bg-rose-50 text-rose-800";
    case "running":
      return "border-amber-200 bg-amber-50 text-amber-900";
    case "skipped":
      return "border-slate-200 bg-slate-50 text-slate-500";
    case "canceled":
      return "border-slate-300 bg-slate-100 text-slate-700";
    case "queued":
    default:
      return "border-slate-200 bg-slate-50 text-slate-700";
  }
}

function checkStatusDotClass(status: string) {
  switch (status) {
    case "passed":
      return "bg-emerald-500";
    case "failed":
    case "errored":
      return "bg-rose-500";
    case "running":
      return "animate-pulse bg-amber-500";
    case "skipped":
    case "canceled":
      return "bg-slate-400";
    case "queued":
    default:
      return "bg-slate-300";
  }
}
