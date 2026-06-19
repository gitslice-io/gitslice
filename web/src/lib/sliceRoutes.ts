import type { SliceRef } from "../api/types";

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
