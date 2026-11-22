import { useEffect, useRef, useState } from "react";

import type { CheckRun, CheckRunLog } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { useConnectStream } from "../../lib/useConnectStream";
import { useStickToBottomScroll } from "../../lib/useStickToBottomScroll";
import { isTerminalCheckStatus } from "./checkStatus";
import { errorMessage } from "./status";
import { AutomationRunLog } from "../../components/automations/AutomationRunLog";

export const MAX_CHECK_LOG_CHARS = 512 * 1024;

export interface CheckLogBuffer {
  text: string;
  chunkCount: number;
  lastSeq: number;
  seenSeqs: ReadonlySet<number>;
}

export function createEmptyCheckLogBuffer(): CheckLogBuffer {
  return {
    text: "",
    chunkCount: 0,
    lastSeq: 0,
    seenSeqs: new Set<number>()
  };
}

export function appendCheckLogChunk(
  current: CheckLogBuffer,
  nextLog: CheckRunLog,
  maxChars: number = MAX_CHECK_LOG_CHARS
): CheckLogBuffer {
  const nextSeq = Number(nextLog.seq ?? 0);
  const validSeq = Number.isFinite(nextSeq) && nextSeq > 0 ? nextSeq : undefined;
  if (validSeq !== undefined && current.seenSeqs.has(validSeq)) {
    return current;
  }
  const nextSeen = new Set(current.seenSeqs);
  if (validSeq !== undefined) {
    nextSeen.add(validSeq);
  }
  const combined = current.text + (nextLog.chunk ?? "");
  const text = combined.length > maxChars ? combined.slice(combined.length - maxChars) : combined;
  return {
    text,
    chunkCount: current.chunkCount + 1,
    lastSeq: validSeq !== undefined ? Math.max(current.lastSeq, validSeq) : current.lastSeq,
    seenSeqs: nextSeen
  };
}

export function CheckRunLogTail({
  api,
  run
}: {
  api: ApiClient;
  run: CheckRun;
}) {
  const [logBuffer, setLogBuffer] = useState<CheckLogBuffer>(createEmptyCheckLogBuffer);
  const preRef = useRef<HTMLPreElement | null>(null);
  const runId = run.id ?? "";

  useEffect(() => {
    setLogBuffer(createEmptyCheckLogBuffer());
  }, [runId]);

  const { streamError, streamClosed } = useConnectStream<CheckRunLog>({
    streamKey: runId,
    attemptDeps: [api],
    enabled: Boolean(runId),
    openStream: (afterSeq, signal) => api.streamCheckRun({ runId, afterSeq }, signal),
    getSeq: (log) => log.seq,
    onItem: (log) => {
      setLogBuffer((current) => appendCheckLogChunk(current, log));
    },
    reconnectOnError: () => !isTerminalCheckStatus(run.status),
    formatError: errorMessage
  });

  const logText = logBuffer.text;
  const live = !isTerminalCheckStatus(run.status) && !streamClosed;

  useStickToBottomScroll({
    containerRef: preRef,
    pinDeps: [logText],
    thresholdPx: 48
  });

  return (
    <div className="border-t border-slate-100 dark:border-zinc-800 bg-slate-950 px-3 py-3 md:px-5">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-semibold",
              live
                ? "bg-emerald-400/10 text-emerald-200"
                : "bg-white/10 dark:bg-zinc-900/10 text-slate-200"
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
          <span className="truncate font-mono text-xs text-slate-400 dark:text-zinc-500">
            {run.checkName || run.id}
          </span>
        </div>
        <span className="font-mono text-xs text-slate-500 dark:text-zinc-400">
          {logBuffer.chunkCount ? `${logBuffer.chunkCount} chunks` : "after_seq=0"}
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
        <div className="rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-slate-400 dark:text-zinc-500">
          {streamClosed
            ? "No log output returned for this run."
            : "Waiting for log output..."}
        </div>
      )}
    </div>
  );
}

// TriggerCheckDetails shows what a trigger reported on a check: its
// annotations and the log of the trigger run that reported it.
export function TriggerCheckDetails({ run }: { run: CheckRun }) {
  const annotations = run.annotations ?? [];
  return (
    <div className="border-t border-slate-100 dark:border-zinc-800">
      {annotations.length > 0 ? (
        <ul aria-label="Annotations" className="divide-y divide-slate-100 dark:divide-zinc-800 px-3 md:px-5">
          {annotations.map((a, i) => (
            <li className="flex gap-2 py-2 text-sm" key={`${a.path}:${a.startLine}:${i}`}>
              <span
                className={cn(
                  "mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full",
                  a.level === "notice" ? "bg-sky-500" : a.level === "warning" ? "bg-amber-500" : "bg-rose-500"
                )}
              />
              <span className="min-w-0">
                <code className="break-all font-mono text-xs text-slate-600 dark:text-zinc-400">
                  {a.path}
                  {a.startLine ? `:${a.startLine}` : ""}
                  {a.endLine && a.endLine !== a.startLine ? `-${a.endLine}` : ""}
                </code>
                <span className="block text-slate-800 dark:text-zinc-200">{a.message}</span>
              </span>
            </li>
          ))}
        </ul>
      ) : null}
      <AutomationRunLog run={{ id: run.automationRunId, status: isTerminalCheckStatus(run.status) ? "succeeded" : "running" }} />
    </div>
  );
}
