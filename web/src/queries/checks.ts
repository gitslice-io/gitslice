import { queryOptions } from "@tanstack/react-query";

import type { CheckRun, TriggerRun } from "../api/types";
import type { ApiClient } from "../api/useApi";

// Check runs and trigger runs of a changeset. Both are written by the SSR
// loader and polled by their panels; a rerun invalidates by the prefixes.

export const checkRunsKeyPrefix = (changesetId: string) => ["checkRuns", changesetId] as const;
export const triggerRunsKey = (changesetId: string) => ["triggerRuns", changesetId] as const;

export function checkRunsQuery(api: Pick<ApiClient, "listCheckRuns">, changesetId: string, patchsetId: string) {
  return queryOptions({
    queryKey: [...checkRunsKeyPrefix(changesetId), patchsetId] as const,
    queryFn: async (): Promise<CheckRun[]> => (await api.listCheckRuns({ changesetId, patchsetId })).runs ?? []
  });
}

export function triggerRunsQuery(api: Pick<ApiClient, "listTriggerRuns">, changesetId: string) {
  return queryOptions({
    queryKey: triggerRunsKey(changesetId),
    queryFn: async (): Promise<TriggerRun[]> => (await api.listTriggerRuns({ changesetId, limit: 50 })).runs ?? []
  });
}
