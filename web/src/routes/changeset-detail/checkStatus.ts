import type { CheckRun } from "../../api/types";

// A check run's status, apart from the panel that shows it: the server render
// needs to know whether a run is finished (see the changeset route's loader)
// without bringing the panel into every page's script.

const terminalCheckStatuses = new Set(["passed", "failed", "errored", "skipped", "canceled"]);

export function normalizeCheckStatus(status?: string) {
  return (status || "queued").toLowerCase();
}

export function isTerminalCheckStatus(status?: string) {
  return terminalCheckStatuses.has(normalizeCheckStatus(status));
}

export function sortCheckRuns(runs: CheckRun[]) {
  return [...runs].sort((left, right) => {
    const leftName = left.checkName ?? "";
    const rightName = right.checkName ?? "";
    if (leftName !== rightName) {
      return leftName.localeCompare(rightName);
    }
    return (left.id ?? "").localeCompare(right.id ?? "");
  });
}

export function shouldShowExitCode(run: CheckRun) {
  const status = normalizeCheckStatus(run.status);
  return (
    typeof run.exitCode === "number" &&
    (status === "passed" || status === "failed" || status === "errored")
  );
}

// safeDetailsUrl allows only http(s) links out of a check.
export function safeDetailsUrl(url?: string) {
  if (!url) return "";
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.toString() : "";
  } catch {
    return "";
  }
}

export function normalizeProvenance(provenance?: string) {
  const normalized = (provenance || "").toLowerCase();
  if (normalized === "self" || normalized === "ci" || normalized === "trigger") {
    return normalized;
  }
  return normalized || "unknown";
}

export function humanizeCheckStatus(status: string) {
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function checkStatusClass(status: string) {
  switch (status) {
    case "passed":
      return "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-800 dark:text-emerald-200";
    case "failed":
    case "errored":
      return "border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 text-rose-800 dark:text-rose-200";
    case "running":
      return "border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 text-amber-900 dark:text-amber-200";
    case "skipped":
      return "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-500 dark:text-zinc-400";
    case "canceled":
      return "border-slate-300 dark:border-zinc-700 bg-slate-100 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300";
    case "queued":
    default:
      return "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-700 dark:text-zinc-300";
  }
}

export function checkStatusDotClass(status: string) {
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
      return "bg-slate-300 dark:bg-zinc-600";
  }
}
