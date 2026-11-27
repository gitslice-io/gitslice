import type { Changeset } from "../../api/types";
import type { Crumb } from "../../components/Breadcrumb";
import {
  humanizeStatus,
  isMergeableStatus,
  isPublishing,
  isTerminalStatus,
  mergeErrorMessage,
  statusClass
} from "../../lib/changesetStatus";
import { getErrorMessage } from "../../lib/errors";
import { changesetLabel } from "../../lib/objectId";
import { formatSliceRef, sliceBreadcrumbItems } from "../../lib/sliceRoutes";

export {
  changesetLabel,
  humanizeStatus,
  isMergeableStatus,
  isPublishing,
  isTerminalStatus,
  mergeErrorMessage,
  statusClass
};

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

export const dangerButtonClass =
  "self-end rounded-md border border-rose-300 bg-white px-4 py-2.5 text-sm font-medium text-rose-700 transition hover:border-rose-500 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-60";
