import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import type { Conversation } from "../api/types";
import { useApi } from "../api/useApi";
import { PageHeader } from "../components/PageHeader";
import {
  ConversationCard,
  sortConversationsNewestFirst
} from "../components/slices/ConversationCard";
import { NewConversationDialog } from "../components/slices/NewConversationDialog";
import { SliceNotice, getErrorMessage } from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";

type StatusFilter = "all" | "active" | "other";

export function ConversationsPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { isLoaded, isSignedIn } = useAuth();
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const [filterText, setFilterText] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");

  const conversationsQuery = useQuery({
    enabled: isLoaded && isSignedIn,
    queryKey: ["allConversations"],
    queryFn: async () => (await api.listConversations({})).conversations ?? []
  });

  const conversations = useMemo(
    () => sortConversationsNewestFirst(conversationsQuery.data ?? []),
    [conversationsQuery.data]
  );

  const filteredConversations = useMemo(() => {
    const normalizedFilter = filterText.trim().toLowerCase();

    return conversations.filter((conversation) => {
      const status = conversation.status || "active";
      if (statusFilter === "active" && status !== "active") {
        return false;
      }
      if (statusFilter === "other" && status === "active") {
        return false;
      }
      if (!normalizedFilter) {
        return true;
      }

      const title =
        conversation.title || conversation.id || "Untitled conversation";
      return [title, conversationSliceLabel(conversation)]
        .join(" ")
        .toLowerCase()
        .includes(normalizedFilter);
    });
  }, [conversations, filterText, statusFilter]);

  function navigateToConversation(conversation: Conversation) {
    const routeParams = toSliceRouteParams(conversation.slice);
    const conversationId = conversation.id;
    if (!routeParams || !conversationId) {
      return;
    }

    void navigate({
      to: "/slices/$account/$slice/agents/$conversationId",
      params: { ...routeParams, conversationId }
    });
  }

  const isLoading =
    !isLoaded || (isLoaded && isSignedIn && conversationsQuery.isPending);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        primaryAction={
          <button
            className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
            onClick={() => setIsCreateOpen(true)}
            type="button"
          >
            New conversation
          </button>
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 sm:text-lg">
            Conversations
          </h1>
        }
      />

      <NewConversationDialog
        onClose={() => setIsCreateOpen(false)}
        onCreated={navigateToConversation}
        open={isCreateOpen}
      />

      <div className="mt-2 grid gap-4">
        <div className="flex flex-col gap-2 rounded-lg border border-slate-200 bg-white p-3 shadow-sm shadow-slate-200/50 sm:flex-row sm:items-center sm:justify-between">
          <input
            aria-label="Filter conversations"
            className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 sm:max-w-sm sm:flex-1"
            onChange={(event) => setFilterText(event.target.value)}
            placeholder="Filter by title or slice"
            value={filterText}
          />
          <select
            aria-label="Filter by status"
            className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 sm:w-40"
            onChange={(event) =>
              setStatusFilter(event.target.value as StatusFilter)
            }
            value={statusFilter}
          >
            <option value="all">All statuses</option>
            <option value="active">Active</option>
            <option value="other">Inactive</option>
          </select>
        </div>

        {isLoading ? (
          <ConversationSkeletonGrid />
        ) : conversationsQuery.isError ? (
          <SliceNotice title="Could not load conversations" tone="error">
            {getErrorMessage(conversationsQuery.error)}
          </SliceNotice>
        ) : filteredConversations.length === 0 ? (
          <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm text-slate-600">
            No conversations yet.
          </div>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {filteredConversations.map((conversation) => (
              <ConversationCard
                conversation={conversation}
                key={conversation.id ?? conversation.createdAt ?? "conversation"}
              />
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

function ConversationSkeletonGrid() {
  return (
    <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
      {[0, 1, 2, 3, 4, 5].map((item) => (
        <div
          aria-hidden
          className="rounded-md border border-slate-200 bg-white px-3 py-2 shadow-sm"
          key={item}
        >
          <div className="h-4 w-3/4 animate-pulse rounded bg-slate-200" />
          <div className="mt-2 h-3 w-1/2 animate-pulse rounded bg-slate-100" />
        </div>
      ))}
    </div>
  );
}

function conversationSliceLabel(conversation: Conversation) {
  const routeParams = toSliceRouteParams(conversation.slice);
  return routeParams
    ? `${routeParams.account}:${routeParams.slice}`
    : "Unknown slice";
}
