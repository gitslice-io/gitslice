import type { SliceRef } from "../api/types";
import type { Crumb } from "../components/Breadcrumb";

export interface SliceRouteParams {
  account: string;
  slice: string;
}

export function toSliceRouteParams(
  ref: SliceRef | null | undefined
): SliceRouteParams | null {
  const account = ref?.account?.trim();
  const slice = ref?.slice?.trim();

  if (!account || !slice) {
    return null;
  }

  return { account, slice };
}

// sliceBreadcrumbItems is a slice's place in a breadcrumb: the owner, linked to
// the owner's page, and then the slice. The owner crumb is how the trail says
// whose slice this is, so it is there for the viewer's own slices too.
export function sliceBreadcrumbItems(
  ref: SliceRef | null | undefined,
  // The viewer's accounts, their personal account first (see useSelection).
  viewerAccounts: readonly string[] | null | undefined
): Crumb[] {
  const params = toSliceRouteParams(ref);
  if (!params) {
    return [];
  }
  return [
    {
      label: `@${params.account}`,
      params: { account: params.account },
      title: accountRelation(params.account, viewerAccounts),
      to: "/accounts/$account"
    },
    { label: params.slice, params, title: `${params.account}:${params.slice}`, to: "/slices/$account/$slice" }
  ];
}

// accountRelation says how an account relates to the viewer, for a tooltip.
export function accountRelation(account: string, viewerAccounts: readonly string[] | null | undefined) {
  const accounts = (viewerAccounts ?? []).map((name) => name.trim().toLowerCase());
  const index = accounts.indexOf(account.trim().toLowerCase());
  if (index === 0) {
    return "Your account";
  }
  return index > 0 ? `${account}, an account you belong to` : `Owned by ${account}`;
}
