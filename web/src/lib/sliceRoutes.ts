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

// sliceBreadcrumbItems is a slice's place in a breadcrumb. A slice in the
// viewer's own account is one crumb, "account:slice". Anyone else's is two, the
// owner and then the slice, so the trail says whose slice this is; the owner has
// no page of its own to link to.
export function sliceBreadcrumbItems(
  ref: SliceRef | null | undefined,
  viewerAccount: string | null | undefined
): Crumb[] {
  const params = toSliceRouteParams(ref);
  if (!params) {
    return [];
  }
  const isOwn = Boolean(viewerAccount) && viewerAccount?.trim().toLowerCase() === params.account.toLowerCase();
  const to = "/slices/$account/$slice";
  if (isOwn) {
    return [{ label: `${params.account}:${params.slice}`, params, to }];
  }
  return [
    { label: `@${params.account}`, title: `Owned by ${params.account}` },
    { label: params.slice, params, title: `${params.account}:${params.slice}`, to }
  ];
}
