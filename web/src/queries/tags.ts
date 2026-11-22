import { queryOptions } from "@tanstack/react-query";

import type { SliceRef } from "../api/types";
import type { ApiClient } from "../api/useApi";

export const sliceTagsKey = (sliceId: string, sliceRef?: SliceRef) =>
  ["sliceTags", sliceId, sliceRef?.account, sliceRef?.slice] as const;

// The slice's tags, shared by the drawer and the pinned-commit banner.
export function sliceTagsQuery(
  api: Pick<ApiClient, "listTags">,
  sliceId: string,
  sliceRef: SliceRef | undefined
) {
  return queryOptions({
    queryKey: sliceTagsKey(sliceId, sliceRef),
    queryFn: () => api.listTags({ slice: sliceRef })
  });
}
