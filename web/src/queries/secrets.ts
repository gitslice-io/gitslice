import { queryOptions } from "@tanstack/react-query";

import type { Secret, SliceRef, SliceSecret } from "../api/types";
import type { ApiClient } from "../api/useApi";

// Account and slice secrets. AccountSecretsPanel, SliceSecretsPanel and
// SliceGitMirrorPanel share these keys so mutations invalidate every secret
// view consistently.

export const secretsKey = (account: string) => ["secrets", account] as const;
export const sliceSecretsKeyPrefix = ["sliceSecrets"] as const;
export const sliceSecretsKey = (slice: SliceRef) =>
  [...sliceSecretsKeyPrefix, slice.account ?? "", slice.slice ?? ""] as const;

export function accountSecretsQuery(api: Pick<ApiClient, "listSecrets">, account: string) {
  return queryOptions({
    queryKey: secretsKey(account),
    queryFn: async (): Promise<Secret[]> => (await api.listSecrets({ account })).secrets ?? []
  });
}

export function sliceSecretsQuery(api: Pick<ApiClient, "listSliceSecrets">, slice: SliceRef) {
  return queryOptions({
    queryKey: sliceSecretsKey(slice),
    queryFn: async (): Promise<SliceSecret[]> => (await api.listSliceSecrets({ slice })).secrets ?? []
  });
}
