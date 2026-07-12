import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo } from "react";

import { useApi } from "../../api/useApi";
import {
  ConversationCard,
  sortConversationsNewestFirst
} from "./ConversationCard";
import { SliceNotice, getErrorMessage } from "./SlicePageParts";

export function RecentConversations() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const enabled = Boolean(isLoaded && isSignedIn);

  const conversationsQuery = useQuery({
    enabled,
    queryKey: ["recentConversations"],
    queryFn: async () => (await api.listConversations({})).conversations ?? []
  });

  const conversations = useMemo(
    () =>
      sortConversationsNewestFirst(conversationsQuery.data ?? []).slice(0, 6),
    [conversationsQuery.data]
  );

  if (isLoaded && !isSignedIn) {
    return null;
  }

  if (!isLoaded) {
    return null;
  }

  return (
    <section>
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
          Recent conversations
        </h2>
        {isSignedIn ? (
          <Link
            className="shrink-0 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-1 text-xs font-medium text-slate-700 dark:text-zinc-300 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50 active:scale-[0.98]"
            to="/conversations"
          >
            View all
          </Link>
        ) : null}
      </div>

      <div className="mt-3">
        {enabled && conversationsQuery.isPending ? (
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {[0, 1, 2].map((item) => (
              <div
                aria-hidden
                className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-2 shadow-sm"
                key={item}
              >
                <div className="h-4 w-3/4 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
                <div className="mt-2 h-3 w-1/2 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
              </div>
            ))}
          </div>
        ) : conversationsQuery.isError ? (
          <SliceNotice title="Could not load conversations" tone="error">
            {getErrorMessage(conversationsQuery.error)}
          </SliceNotice>
        ) : conversations.length === 0 ? (
          <div className="rounded-md border border-dashed border-slate-300 dark:border-zinc-700 p-4 text-sm text-slate-600 dark:text-zinc-400">
            No conversations yet.
          </div>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {conversations.map((conversation) => (
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
