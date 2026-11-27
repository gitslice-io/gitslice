import { queryOptions } from "@tanstack/react-query";

export interface ServiceHealthPayload {
  overall: "operational" | "degraded";
  checkedAt: string;
  api: {
    status: "operational" | "degraded";
    latencyMs: number | null;
    httpStatus: number | null;
  };
}

export const statusHealthKey = ["statusHealth"] as const;

export function statusHealthQuery() {
  return queryOptions({
    queryKey: statusHealthKey,
    queryFn: async ({ signal }): Promise<ServiceHealthPayload> => {
      const res = await fetch("/api/status", {
        method: "GET",
        cache: "no-store",
        signal
      });
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }
      return (await res.json()) as ServiceHealthPayload;
    },
    retry: false,
    staleTime: 0
  });
}
