import { queryOptions } from "@tanstack/react-query";

import type { CheckRun, AutomationRun, AutomationRunLog } from "../api/types";
import type { ApiClient } from "../api/useApi";

// Check runs and trigger runs of a changeset, plus trigger run logs. Written by
// the SSR loader and polled by their panels; reruns invalidate by the prefixes.

export const checkRunsAllKeyPrefix = ["checkRuns"] as const;
export const checkRunsKeyPrefix = (changesetId: string) => ["checkRuns", changesetId] as const;
export const triggerRunsKey = (changesetId: string) => ["triggerRuns", changesetId] as const;
export const triggerRunLogsKey = (runId: string) => ["triggerRunLogs", runId] as const;

export function checkRunsQuery(api: Pick<ApiClient, "listCheckRuns">, changesetId: string, patchsetId: string) {
  return queryOptions({
    queryKey: [...checkRunsKeyPrefix(changesetId), patchsetId] as const,
    queryFn: async (): Promise<CheckRun[]> => (await api.listCheckRuns({ changesetId, patchsetId })).runs ?? []
  });
}

export function triggerRunsQuery(api: Pick<ApiClient, "listAutomationRuns">, changesetId: string) {
  return queryOptions({
    queryKey: triggerRunsKey(changesetId),
    queryFn: async (): Promise<AutomationRun[]> => (await api.listAutomationRuns({ changesetId, limit: 50 })).runs ?? []
  });
}

export function triggerRunLogsQuery(api: Pick<ApiClient, "listAutomationRunLogs">, runId: string) {
  return queryOptions({
    queryKey: triggerRunLogsKey(runId),
    queryFn: async (): Promise<AutomationRunLog[]> => (await api.listAutomationRunLogs({ runId, afterSeq: "0" })).logs ?? []
  });
}
