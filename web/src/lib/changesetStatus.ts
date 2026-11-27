import { isTransientTransportError } from "../api/rpcError";
import { getErrorMessage } from "./errors";

export function humanizeStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  switch (normalized) {
    case "":
    case "draft":
      return "Draft";
    case "open":
      return "Open";
    case "pending_publish":
      return "Publishing";
    case "published":
      return "Published";
    case "submitted":
      return "Submitted";
    case "merged":
      return "Merged";
    case "abandoned":
      return "Abandoned";
    default:
      return status || "Unknown";
  }
}

export function isPublishing(status?: string) {
  return (status || "").toLowerCase() === "pending_publish";
}

export function statusClass(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "published":
    case "merged":
    case "submitted":
      return "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800/80 dark:bg-emerald-950/45 dark:text-emerald-300";
    case "pending_publish":
      return "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-800/80 dark:bg-amber-950/45 dark:text-amber-200";
    case "abandoned":
      return "border-rose-200 bg-rose-50 text-rose-800 dark:border-rose-800/80 dark:bg-rose-950/45 dark:text-rose-300";
    case "draft":
      return "border-slate-200 bg-slate-50 text-slate-700 dark:border-zinc-700 dark:bg-zinc-800/70 dark:text-zinc-300";
    default:
      return "border-slate-200 bg-slate-50 text-slate-700 dark:border-zinc-700 dark:bg-zinc-800/70 dark:text-zinc-300";
  }
}

export function isTerminalStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return (
    normalized === "submitted" ||
    normalized === "pending_publish" ||
    normalized === "published" ||
    normalized === "merged" ||
    normalized === "abandoned"
  );
}

export function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}

const mergeIncompleteMessage =
  "The merge request did not complete. Large changesets can take a couple of minutes — keep this tab in the foreground and try again.";

// Merge submits can legitimately run for minutes on large changesets; an
// aborted or timed-out request needs guidance, not a raw fetch error.
export function mergeErrorMessage(error: unknown): string {
  if (error instanceof TypeError || isTransientTransportError(error)) {
    return mergeIncompleteMessage;
  }
  return getErrorMessage(error, "Request failed.");
}
