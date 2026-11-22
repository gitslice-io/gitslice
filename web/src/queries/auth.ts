import { queryOptions } from "@tanstack/react-query";

import type { AgentInvite, OwnedAgent, SharedSlice } from "../api/types";
import type { ApiClient } from "../api/useApi";

// Who the viewer is, what is shared with them, and the agents they claim or
// co-own. The SSR route loaders, the selection provider, the top-bar notice and
// the claims/account pages read these through the same options, so each key and
// cached shape is written once.

export const authStatusKey = ["authStatus"] as const;
export const sharedSlicesKey = ["sharedSlices"] as const;
export const pendingClaimsKey = ["pendingClaims"] as const;
export const ownedAgentsKey = ["ownedAgents"] as const;
export const agentInvitesKey = ["agentInvites"] as const;
export const myInvitationsKey = ["myInvitations"] as const;
export const usernameAvailableKey = (username: string) => ["usernameAvailable", username] as const;

export function authStatusQuery(api: Pick<ApiClient, "getAuthStatus">) {
  return queryOptions({
    queryKey: authStatusKey,
    queryFn: () => api.getAuthStatus({})
  });
}

// Slices other accounts shared with the viewer, with the role on each. A
// server that lacks the call, or a test client without it, shares nothing.
export function sharedSlicesQuery(api: Partial<Pick<ApiClient, "listSharedSlices">>) {
  return queryOptions({
    queryKey: sharedSlicesKey,
    queryFn: async (): Promise<SharedSlice[]> => {
      if (typeof api.listSharedSlices !== "function") {
        return [];
      }
      return (await api.listSharedSlices({})).slices ?? [];
    }
  });
}

export function usernameAvailableQuery(
  api: Pick<ApiClient, "checkUsernameAvailable">,
  username: string
) {
  return queryOptions({
    queryKey: usernameAvailableKey(username),
    queryFn: () => api.checkUsernameAvailable({ username })
  });
}

export function pendingClaimsQuery(api: Pick<ApiClient, "listPendingClaims">) {
  return queryOptions({
    queryKey: pendingClaimsKey,
    queryFn: () => api.listPendingClaims({}),
    retry: false
  });
}

export function ownedAgentsQuery(api: Pick<ApiClient, "listOwnedAgents">) {
  return queryOptions({
    queryKey: ownedAgentsKey,
    queryFn: async (): Promise<OwnedAgent[]> => (await api.listOwnedAgents({})).agents ?? [],
    retry: false
  });
}

export function agentInvitesQuery(api: Partial<Pick<ApiClient, "listAgentInvites">>) {
  return queryOptions({
    queryKey: agentInvitesKey,
    queryFn: async (): Promise<AgentInvite[]> => {
      if (typeof api.listAgentInvites !== "function") {
        return [];
      }
      return (await api.listAgentInvites({})).invites ?? [];
    },
    retry: false
  });
}
