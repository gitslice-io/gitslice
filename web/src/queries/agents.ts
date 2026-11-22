import { queryOptions } from "@tanstack/react-query";

import type { AgentDaemon, Conversation, SliceRef } from "../api/types";
import type { ApiClient } from "../api/useApi";

// Agent daemons and conversations. The cache holds the arrays and the
// conversation itself, not the wire-level responses; every setQueryData goes
// through these keys so the shape written is the shape read (a response
// wrapper once crashed the Agents tab by landing where the array belongs).

export const agentDaemonsKey = ["agentDaemons"] as const;
export const sliceConversationsKeyPrefix = ["sliceConversations"] as const;
export const recentConversationsKey = ["recentConversations"] as const;
export const allConversationsKey = ["allConversations"] as const;
export const conversationKey = (conversationId: string) => ["conversation", conversationId] as const;
export const conversationEventsKey = (
  conversationId: string,
  afterSeq: number | string,
  beforeSeq: number | string
) => ["conversationEvents", conversationId, afterSeq, beforeSeq] as const;

export function sliceConversationsKey(slice: SliceRef) {
  return [...sliceConversationsKeyPrefix, `${slice.account ?? ""}:${slice.slice ?? ""}`] as const;
}

export function agentDaemonsQuery(api: Pick<ApiClient, "listDaemons">) {
  return queryOptions({
    queryKey: agentDaemonsKey,
    queryFn: async (): Promise<AgentDaemon[]> => (await api.listDaemons({})).daemons ?? []
  });
}

// The daemons that can run a slice's checks, for its settings page; under
// the daemons' prefix, so a change to daemons refreshes it too.
export function sliceDaemonsQuery(api: Pick<ApiClient, "listDaemons">, slice: SliceRef) {
  return queryOptions({
    queryKey: [...agentDaemonsKey, "forSlice", slice.account ?? "", slice.slice ?? ""] as const,
    queryFn: async (): Promise<AgentDaemon[]> => (await api.listDaemons({ forSlice: slice })).daemons ?? []
  });
}

export function sliceConversationsQuery(api: Pick<ApiClient, "listConversations">, slice: SliceRef) {
  return queryOptions({
    queryKey: sliceConversationsKey(slice),
    queryFn: async (): Promise<Conversation[]> => (await api.listConversations({ slice })).conversations ?? []
  });
}

export function recentConversationsQuery(api: Pick<ApiClient, "listConversations">) {
  return queryOptions({
    queryKey: recentConversationsKey,
    queryFn: async (): Promise<Conversation[]> => (await api.listConversations({})).conversations ?? []
  });
}

export function allConversationsQuery(api: Pick<ApiClient, "listConversations">) {
  return queryOptions({
    queryKey: allConversationsKey,
    queryFn: async (): Promise<Conversation[]> => (await api.listConversations({})).conversations ?? []
  });
}

export function conversationQuery(api: Pick<ApiClient, "getConversation">, conversationId: string) {
  return queryOptions({
    queryKey: conversationKey(conversationId),
    queryFn: () => api.getConversation({ conversationId })
  });
}
