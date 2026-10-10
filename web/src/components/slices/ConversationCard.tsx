import { Link } from "@tanstack/react-router";

import type { Conversation } from "../../api/types";
import { Timestamp } from "../Timestamp";
import { cn } from "../../lib/cn";
import { toSliceRouteParams } from "../../lib/sliceRoutes";

export function ConversationCard({
  conversation
}: {
  conversation: Conversation;
}) {
  const routeParams = toSliceRouteParams(conversation.slice);
  const conversationId = conversation.id ?? "";
  const sliceLabel = routeParams
    ? `${routeParams.account}:${routeParams.slice}`
    : "Unknown slice";
  const title =
    conversation.title || conversation.id || "Untitled conversation";
  const timeSource = conversation.updatedAt || conversation.createdAt;
  const cardClass =
    "block rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3.5 py-3 shadow-sm shadow-slate-200/50 dark:shadow-black/50 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950";
  const body = (
    <>
      <p className="truncate text-sm font-semibold text-zinc-950 dark:text-zinc-50">{title}</p>
      <div className="mt-1.5 flex min-w-0 items-center gap-2 text-xs text-slate-500 dark:text-zinc-400">
        <ConversationStatusPill conversation={conversation} />
        <span className="min-w-0 truncate">{sliceLabel}</span>
        <Timestamp
          className="ml-auto shrink-0 whitespace-nowrap font-mono text-[0.7rem] text-slate-400 dark:text-zinc-500"
          relative
          value={timeSource}
        />
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

export function ConversationStatusPill({
  conversation
}: {
  conversation: Conversation;
}) {
  const value = conversation.status || "active";
  const active = value === "active";

  return (
    <span
      aria-label={value}
      className={cn(
        "inline-block h-2 w-2 shrink-0 rounded-full ring-2",
        active
          ? "bg-emerald-500 ring-emerald-500/15"
          : "bg-slate-300 dark:bg-zinc-600 ring-slate-300/20"
      )}
      role="img"
      title={value}
    />
  );
}

// Kept for existing imports; times are formatted in src/lib/time.ts.
export { formatRelativeTime } from "../../lib/time";

export function sortConversationsNewestFirst(conversations: Conversation[]) {
  return [...conversations].sort((left, right) => {
    const leftTime = conversationSortTime(left);
    const rightTime = conversationSortTime(right);
    if (leftTime !== rightTime) {
      return rightTime - leftTime;
    }
    return (right.id ?? "").localeCompare(left.id ?? "");
  });
}

export function conversationSortTime(conversation: Conversation) {
  const updated = Date.parse(conversation.updatedAt ?? "");
  if (Number.isFinite(updated)) {
    return updated;
  }
  const created = Date.parse(conversation.createdAt ?? "");
  return Number.isFinite(created) ? created : 0;
}
