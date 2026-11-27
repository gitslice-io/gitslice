import { isTransientTransportError } from "../../api/rpcError";
import type { Changeset } from "../../api/types";
import type { Crumb } from "../../components/Breadcrumb";
import { getErrorMessage } from "../../lib/errors";
import { changesetLabel } from "../../lib/objectId";
import { formatSliceRef, sliceBreadcrumbItems } from "../../lib/sliceRoutes";

export { changesetLabel };

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

export function changesetSliceSearch(changeset: Changeset) {
  return formatSliceRef(changeset.authoringSlice);
}

export function changesetBreadcrumbItems({
  changeset,
  sliceSearch,
  viewerAccounts
}: {
  changeset: Changeset;
  sliceSearch: string;
  viewerAccounts?: readonly string[];
}): Crumb[] {
  const items: Crumb[] = [{ label: "Home", to: "/" }];

  if (sliceSearch) {
    const sliceCrumbs = sliceBreadcrumbItems(changeset.authoringSlice, viewerAccounts);
    items.push(...(sliceCrumbs.length > 0 ? sliceCrumbs : [{ label: sliceSearch }]));
    items.push({
      label: `${sliceSearch} changesets`,
      search: { slice: sliceSearch },
      to: "/changesets"
    });
  }

  items.push({ label: changesetLabel(changeset) });

  return items;
}

export function errorMessage(error: unknown) {
  return getErrorMessage(error, "Request failed.");
}

// Merge submits can legitimately run for minutes on large changesets; an
// aborted or timed-out request needs guidance, not a raw fetch error.
export function mergeErrorMessage(error: unknown): string {
  if (error instanceof TypeError || isTransientTransportError(error)) {
    return mergeIncompleteMessage;
  }
  return errorMessage(error);
}

const mergeIncompleteMessage =
  "The merge request did not complete. Large changesets can take a couple of minutes — keep this tab in the foreground and try again.";

export const dangerButtonClass =
  "self-end rounded-md border border-rose-300 bg-white px-4 py-2.5 text-sm font-medium text-rose-700 transition hover:border-rose-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";
