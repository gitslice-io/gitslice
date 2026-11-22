import { queryOptions } from "@tanstack/react-query";

import type { ApiClient } from "../api/useApi";

export const folderPolicyKey = (folder: string) => ["folderPolicy", folder] as const;
export const automationRunStepsKey = (runId: string) => ["automationRunSteps", runId] as const;
export const triggerRunsOfKey = (account: string, automationId?: string) =>
  ["triggerRunsOf", account, automationId] as const;

export function folderPolicyQuery(api: Pick<ApiClient, "getFolderPolicy">, folder: string) {
  return queryOptions({
    queryKey: folderPolicyKey(folder),
    queryFn: () => api.getFolderPolicy({ path: folder })
  });
}

export function automationRunStepsQuery(api: Pick<ApiClient, "listAutomationRuns">, runId: string) {
  return queryOptions({
    queryKey: automationRunStepsKey(runId),
    queryFn: async () => (await api.listAutomationRuns({ parentRunId: runId, limit: 200 })).runs ?? []
  });
}

export function triggerRunsOfQuery(
  api: Pick<ApiClient, "listAutomationRuns">,
  account: string,
  automationId?: string
) {
  return queryOptions({
    queryKey: triggerRunsOfKey(account, automationId),
    queryFn: async () => (await api.listAutomationRuns({ account, automationId, limit: 5 })).runs ?? []
  });
}
