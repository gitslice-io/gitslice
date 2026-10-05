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
  viewerAccount: string | null | undefined
): Crumb[] {
  const params = toSliceRouteParams(ref);
  if (!params) {
    return [];
  }
  const isOwn = Boolean(viewerAccount) && viewerAccount?.trim().toLowerCase() === params.account.toLowerCase();
  return [
    {
      label: `@${params.account}`,
      params: { account: params.account },
      title: isOwn ? "Your account" : `Owned by ${params.account}`,
      to: "/accounts/$account"
    },
    { label: params.slice, params, title: `${params.account}:${params.slice}`, to: "/slices/$account/$slice" }
  ];
}
