import type { Changeset } from "../../api/types";
import type { Crumb } from "../../components/Breadcrumb";
import { shortChangesetId } from "../../lib/objectId";
import { toSliceRouteParams } from "../../lib/sliceRoutes";

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
      return "border-emerald-200 bg-emerald-50 text-emerald-800";
    case "pending_publish":
      return "border-amber-200 bg-amber-50 text-amber-900";
    case "abandoned":
      return "border-rose-200 bg-rose-50 text-rose-800";
    case "draft":
      return "border-slate-200 bg-slate-50 text-slate-700";
    default:
      return "border-slate-200 bg-slate-50 text-slate-700";
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
  const ref = changeset.authoringSlice;
  if (!ref?.account || !ref.slice) {
    return "";
  }
  return `${ref.account}:${ref.slice}`;
}

export function changesetBreadcrumbItems({
  changeset,
  sliceSearch
}: {
  changeset: Changeset;
  sliceSearch: string;
}): Crumb[] {
  const items: Crumb[] = [{ label: "Home", to: "/" }];

  if (sliceSearch) {
    const routeParams = toSliceRouteParams(changeset.authoringSlice);
    items.push(
      routeParams
        ? {
            label: sliceSearch,
            params: routeParams,
            to: "/slices/$account/$slice"
          }
        : { label: sliceSearch }
    );
    items.push({
      label: "Changesets",
      search: { slice: sliceSearch },
      to: "/changesets"
    });
  }

  items.push({ label: changesetLabel(changeset) });

  return items;
}

export function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "changeset";
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

export const dangerButtonClass =
  "self-end rounded-md border border-rose-300 bg-white px-4 py-2.5 text-sm font-medium text-rose-700 transition hover:border-rose-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";
