import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo } from "react";

import type { Conversation } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { toSliceRouteParams } from "../../lib/sliceRoutes";
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
      <h2 className="text-sm font-semibold text-zinc-950">
        Recent conversations
      </h2>

      <div className="mt-3">
        {enabled && conversationsQuery.isPending ? (
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {[0, 1, 2].map((item) => (
              <div
                aria-hidden
                className="rounded-lg border border-slate-200 bg-white p-3 shadow-sm"
                key={item}
              >
                <div className="h-4 w-3/4 animate-pulse rounded bg-slate-200" />
                <div className="mt-3 h-3 w-1/2 animate-pulse rounded bg-slate-100" />
                <div className="mt-4 h-5 w-2/3 animate-pulse rounded bg-slate-100" />
              </div>
            ))}
          </div>
        ) : conversationsQuery.isError ? (
          <SliceNotice title="Could not load conversations" tone="error">
            {getErrorMessage(conversationsQuery.error)}
          </SliceNotice>
        ) : conversations.length === 0 ? (
          <div className="rounded-md border border-dashed border-slate-300 p-4 text-sm text-slate-600">
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

function ConversationCard({ conversation }: { conversation: Conversation }) {
  const routeParams = toSliceRouteParams(conversation.slice);
  const conversationId = conversation.id ?? "";
  const sliceLabel = routeParams
    ? `${routeParams.account}:${routeParams.slice}`
    : "Unknown slice";
  const title =
    conversation.title || conversation.id || "Untitled conversation";
  const timeSource = conversation.updatedAt || conversation.createdAt;
  const timeLabel = formatRelativeTime(timeSource);
  const cardClass =
    "rounded-lg border border-slate-200 bg-white p-3 shadow-sm transition hover:border-slate-300";
  const body = (
    <>
      <p className="truncate font-semibold text-zinc-950">{title}</p>
      <p className="mt-1 truncate text-xs text-slate-500">{sliceLabel}</p>
      <div className="mt-3 flex min-w-0 items-center gap-2 text-xs text-slate-500">
        <ConversationStatusPill conversation={conversation} />
        <span
          className="truncate font-mono text-[0.7rem] text-slate-400"
          title={timeSource}
        >
          {timeLabel}
        </span>
      </div>
    </>
  );

  if (!routeParams || !conversationId) {
    return <div className={cardClass}>{body}</div>;
  }

  return (
    <Link
      className={cardClass}
      params={{ ...routeParams, conversationId }}
      to="/slices/$account/$slice/agents/$conversationId"
    >
      {body}
    </Link>
  );
}

function ConversationStatusPill({
  conversation
}: {
  conversation: Conversation;
}) {
  const value =
    conversation.status ||
    (conversation.daemonOnline === false ? "offline" : "active");
  const normalized = value.toLowerCase();
  const tone =
    normalized === "active" || normalized === "online"
      ? "bg-emerald-50 text-emerald-700"
      : normalized === "error" || normalized === "failed"
        ? "bg-rose-50 text-rose-700"
        : "bg-slate-100 text-slate-600";

  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 text-[0.65rem] font-semibold uppercase tracking-normal",
        tone
      )}
    >
      {value}
    </span>
  );
}

function formatRelativeTime(iso?: string) {
  if (!iso) {
    return "unknown";
  }

  const then = Date.parse(iso);
  if (!Number.isFinite(then)) {
    return "unknown";
  }
  const seconds = Math.round((Date.now() - then) / 1000);
  if (seconds < 45) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 7) return `${days}d ago`;
  return new Date(then).toISOString().slice(0, 10);
}

function sortConversationsNewestFirst(conversations: Conversation[]) {
  return [...conversations].sort((left, right) => {
    const leftTime = conversationSortTime(left);
    const rightTime = conversationSortTime(right);
    if (leftTime !== rightTime) {
      return rightTime - leftTime;
    }
    return (right.id ?? "").localeCompare(left.id ?? "");
  });
}

function conversationSortTime(conversation: Conversation) {
  const updated = Date.parse(conversation.updatedAt ?? "");
  if (Number.isFinite(updated)) {
    return updated;
  }
  const created = Date.parse(conversation.createdAt ?? "");
  return Number.isFinite(created) ? created : 0;
}
