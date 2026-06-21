import { FormEvent, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type {
  AgentDaemon,
  Conversation,
  SliceRef
} from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { AgentConversation } from "./AgentConversation";
import { SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

interface AgentsTabProps {
  api: ApiClient;
  slice: SliceRef;
}

export function AgentsTab({ api, slice }: AgentsTabProps) {
  const queryClient = useQueryClient();
  const sliceKey = `${slice.account ?? ""}:${slice.slice ?? ""}`;
  const sliceDefined = Boolean(slice.account && slice.slice);

  const daemonsQuery = useQuery({
    enabled: sliceDefined,
    queryKey: ["agentDaemons"],
    queryFn: async () => (await api.listDaemons({})).daemons ?? []
  });

  const conversationsQuery = useQuery({
    enabled: sliceDefined,
    queryKey: ["sliceConversations", sliceKey],
    queryFn: async () =>
      (await api.listConversations({ slice })).conversations ?? []
  });

  const daemons = daemonsQuery.data ?? [];
  const onlineDaemons = useMemo(
    () => daemons.filter((daemon) => daemon.status === "online"),
    [daemons]
  );
  const conversations = useMemo(
    () => sortConversationsNewestFirst(conversationsQuery.data ?? []),
    [conversationsQuery.data]
  );

  const [selectedConversationId, setSelectedConversationId] = useState("");
  const [selectedDaemonId, setSelectedDaemonId] = useState("");
  const [title, setTitle] = useState("");
  const [isSidebarOpen, setIsSidebarOpen] = useState(true);
  // Mobile drawer state, separate from the desktop column collapse. On small
  // screens the conversation list slides in from the left like the file tree;
  // on lg+ it is a static column governed by isSidebarOpen.
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const [isCreateOpen, setIsCreateOpen] = useState(false);

  // Keep daemon selection valid as the online set changes. We only reassign
  // when the current pick is gone; we never clobber a user-chosen daemon.
  useEffect(() => {
    setSelectedDaemonId((current) =>
      onlineDaemons.some((daemon) => daemon.id === current)
        ? current
        : (onlineDaemons[0]?.id ?? "")
    );
  }, [onlineDaemons]);

  // Default-select the newest conversation, but never overwrite an active
  // selection that still exists in the list.
  useEffect(() => {
    setSelectedConversationId((current) => {
      if (current && conversations.some((c) => c.id === current)) {
        return current;
      }
      return conversations[0]?.id ?? "";
    });
  }, [conversations]);

  const selectedConversation = conversations.find(
    (conversation) => conversation.id === selectedConversationId
  );

  const createMutation = useMutation({
    mutationFn: (input: { daemonId: string; title: string }) =>
      api.createConversation({
        daemonId: input.daemonId,
        slice,
        title: input.title || undefined
      }),
    onSuccess: (conversation) => {
      // Optimistically prepend so the user sees their conversation instantly;
      // the next refetch will reconcile against server truth. The query cache
      // stores the array directly (the queryFn returns Conversation[]), so we
      // must update with an array too — not the wire-level response object.
      queryClient.setQueryData<Conversation[]>(
        ["sliceConversations", sliceKey],
        (current) => [
          conversation,
          ...(current ?? []).filter((item) => item.id !== conversation.id)
        ]
      );
      setSelectedConversationId(conversation.id ?? "");
      setTitle("");
      setIsCreateOpen(false);
      setIsSidebarOpen(true);
      // On mobile, close the drawer so the freshly created chat is in view.
      setMobileSidebarOpen(false);
    }
  });

  async function createConversation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedDaemonId) {
      return;
    }
    createMutation.mutate({ daemonId: selectedDaemonId, title: title.trim() });
  }

  if (!sliceDefined) {
    return (
      <SliceNotice title="Missing slice" tone="error">
        Agent conversations need an account and slice name.
      </SliceNotice>
    );
  }

  if (daemonsQuery.isPending && conversationsQuery.isPending) {
    return (
      <SlicePanel className="min-h-[28rem]">
        <div className="grid gap-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
          <div className="grid gap-3">
            <div className="h-8 animate-pulse rounded-md bg-slate-200" />
            <div className="h-20 animate-pulse rounded-md bg-slate-100" />
            <div className="h-20 animate-pulse rounded-md bg-slate-100" />
          </div>
          <div className="h-72 animate-pulse rounded-md bg-slate-100" />
        </div>
      </SlicePanel>
    );
  }

  const loadError =
    (daemonsQuery.error && getErrorMessage(daemonsQuery.error)) ||
    (conversationsQuery.error && getErrorMessage(conversationsQuery.error));

  if (loadError) {
    return (
      <SliceNotice title="Could not load agents" tone="error">
        {loadError}
      </SliceNotice>
    );
  }

  const sidebarToggle = (
    <div className="flex items-center gap-2">
      {/* Mobile: open the conversation list as a left drawer. */}
      <button
        aria-controls="agent-conversation-sidebar"
        aria-expanded={mobileSidebarOpen}
        className="rounded-md border border-slate-200 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 transition hover:border-slate-300 hover:bg-slate-50 hover:text-zinc-950 active:scale-[0.98] lg:hidden"
        onClick={() => setMobileSidebarOpen(true)}
        type="button"
      >
        Conversations
      </button>
      {/* Desktop: collapse/expand the static left column. */}
      <button
        aria-controls="agent-conversation-sidebar"
        aria-expanded={isSidebarOpen}
        className="hidden rounded-md border border-slate-200 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 transition hover:border-slate-300 hover:bg-slate-50 hover:text-zinc-950 active:scale-[0.98] lg:inline-flex"
        onClick={() => setIsSidebarOpen((open) => !open)}
        type="button"
      >
        {isSidebarOpen ? "Hide conversations" : "Show conversations"}
      </button>
    </div>
  );

  const createError = createMutation.error
    ? getErrorMessage(createMutation.error)
    : "";
  const isCreating = createMutation.isPending;

  return (
    <SlicePanel className="h-full min-h-0 overflow-hidden p-0">
      <div
        className={cn(
          "grid h-full min-h-0 gap-0",
          isSidebarOpen
            ? "lg:grid-cols-[20rem_minmax(0,1fr)]"
            : "lg:grid-cols-[minmax(0,1fr)]"
        )}
      >
        {mobileSidebarOpen ? (
          <button
            aria-label="Close conversations"
            className="fixed inset-0 z-30 bg-black/30 lg:hidden"
            onClick={() => setMobileSidebarOpen(false)}
            type="button"
          />
        ) : null}
        {isSidebarOpen || mobileSidebarOpen ? (
          <aside
            aria-label="Agent conversations"
            className={cn(
              "fixed inset-y-0 left-0 z-40 w-80 max-w-[85%] transform overflow-y-auto bg-white p-4 shadow-xl transition-transform duration-200",
              mobileSidebarOpen ? "translate-x-0" : "-translate-x-full",
              "lg:static lg:z-auto lg:w-auto lg:max-w-none lg:translate-x-0 lg:bg-transparent lg:p-5 lg:shadow-none lg:transition-none lg:min-h-0 lg:overflow-y-auto lg:border-r lg:border-slate-200",
              isSidebarOpen ? "" : "lg:hidden"
            )}
            id="agent-conversation-sidebar"
          >
            <div className="mb-3 flex justify-end lg:hidden">
              <button
                aria-label="Close conversations"
                className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
                onClick={() => setMobileSidebarOpen(false)}
                type="button"
              >
                Close
              </button>
            </div>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h2 className="text-sm font-semibold text-zinc-950">
                  Conversations
                </h2>
                <p className="mt-1 truncate text-xs leading-5 text-slate-500">
                  {sliceKey}
                </p>
              </div>
              <button
                className="shrink-0 rounded-md bg-zinc-950 px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
                disabled={!onlineDaemons.length || isCreating}
                onClick={() => {
                  // Clear stale mutation errors so the form reopens cleanly.
                  createMutation.reset();
                  setIsCreateOpen((open) => !open);
                }}
                type="button"
              >
                {isCreateOpen ? "Cancel" : "New conversation"}
              </button>
            </div>

            {isCreateOpen && onlineDaemons.length ? (
              <form className="mt-4 grid gap-3" onSubmit={createConversation}>
                <label className="grid gap-1.5 text-sm font-medium text-zinc-800">
                  Agent daemon
                  <select
                    className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
                    onChange={(event) =>
                      setSelectedDaemonId(event.target.value)
                    }
                    value={selectedDaemonId}
                  >
                    {onlineDaemons.map((daemon) => (
                      <option
                        key={daemon.id ?? daemon.name}
                        value={daemon.id ?? ""}
                      >
                        {daemon.name || daemon.id}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="grid gap-1.5 text-sm font-medium text-zinc-800">
                  Title
                  <input
                    className="min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
                    onChange={(event) => setTitle(event.target.value)}
                    placeholder="Optional"
                    value={title}
                  />
                </label>
                {createError ? (
                  <p className="text-sm text-rose-700">{createError}</p>
                ) : null}
                <button
                  className="rounded-md bg-zinc-950 px-3 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
                  disabled={!selectedDaemonId || isCreating}
                  type="submit"
                >
                  {isCreating ? "Creating..." : "Create conversation"}
                </button>
              </form>
            ) : null}

            <div className="mt-5">
              <h3 className="text-xs font-semibold uppercase tracking-normal text-slate-500">
                Daemons
              </h3>
              {onlineDaemons.length === 0 ? (
                <div className="mt-3 rounded-md border border-dashed border-slate-300 bg-slate-50 p-4 text-sm text-slate-700">
                  <p className="font-semibold text-zinc-950">
                    No online daemons
                  </p>
                  <p className="mt-2 leading-6">
                    Run{" "}
                    <code className="rounded bg-white px-1.5 py-0.5 font-mono text-xs text-slate-800">
                      gs agent start
                    </code>{" "}
                    in an empty directory to make an agent available here.
                  </p>
                </div>
              ) : (
                <div className="mt-3 grid grid-cols-1 gap-2">
                  {onlineDaemons.map((daemon) => (
                    <DaemonRow daemon={daemon} key={daemon.id ?? daemon.name} />
                  ))}
                </div>
              )}
            </div>

            <div className="mt-5 border-t border-slate-200 pt-4">
              <div className="flex items-center justify-between gap-3">
                <h3 className="text-xs font-semibold uppercase tracking-normal text-slate-500">
                  History
                </h3>
                {conversations.length ? (
                  <span className="font-mono text-xs text-slate-400">
                    {conversations.length}
                  </span>
                ) : null}
              </div>
              {conversations.length === 0 ? (
                <p className="mt-3 rounded-md border border-dashed border-slate-300 p-3 text-sm text-slate-600">
                  No conversations yet.
                </p>
              ) : (
                <ul className="mt-3 grid grid-cols-1 gap-1.5">
                  {conversations.map((conversation) => {
                    const isSelected =
                      conversation.id === selectedConversationId;
                    return (
                      <li key={conversation.id}>
                        <button
                          aria-current={isSelected ? "true" : undefined}
                          className={cn(
                            "min-w-0 w-full rounded-md px-3 py-2 text-left text-sm transition active:scale-[0.98]",
                            isSelected
                              ? "bg-slate-100 text-zinc-950"
                              : "text-slate-700 hover:bg-slate-50 hover:text-zinc-950"
                          )}
                          onClick={() => {
                            setSelectedConversationId(conversation.id ?? "");
                            // Close the mobile drawer so the chat is revealed.
                            setMobileSidebarOpen(false);
                          }}
                          type="button"
                        >
                          <span className="block truncate font-semibold">
                            {conversation.title ||
                              conversation.id ||
                              "Untitled conversation"}
                          </span>
                          <span className="mt-1 flex items-center gap-2">
                            <ConversationStatusPill
                              status={conversation.status}
                            />
                            {conversation.updatedAt ? (
                              <span
                                className="truncate font-mono text-[0.7rem] text-slate-400"
                                title={conversation.updatedAt}
                              >
                                {formatRelativeTime(conversation.updatedAt)}
                              </span>
                            ) : null}
                          </span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          </aside>
        ) : null}

        <div className="min-h-0 min-w-0">
          {selectedConversationId ? (
            <AgentConversation
              api={api}
              conversation={selectedConversation}
              conversationId={selectedConversationId}
              toolbar={sidebarToggle}
            />
          ) : (
            <div className="flex h-full min-h-0 flex-col">
              <div className="border-b border-slate-200 px-4 py-4 sm:px-5">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <h2 className="text-base font-semibold text-zinc-950">
                    No conversation selected
                  </h2>
                  {sidebarToggle}
                </div>
              </div>
              <div className="flex flex-1 items-center justify-center p-6 text-center">
                <div className="max-w-sm">
                  <p className="text-sm leading-6 text-slate-600">
                    {onlineDaemons.length
                      ? "Start a new conversation with an online daemon, or pick one from the sidebar."
                      : "Run gs agent start to make an agent available, then create a conversation."}
                  </p>
                  {onlineDaemons.length ? (
                    <button
                      className="mt-4 rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
                      onClick={() => {
                        setIsSidebarOpen(true);
                        setMobileSidebarOpen(true);
                        setIsCreateOpen(true);
                      }}
                      type="button"
                    >
                      New conversation
                    </button>
                  ) : null}
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </SlicePanel>
  );
}

// Local components kept stateless so they don't re-render when the parent's
// selection state changes. Each row only depends on its own props.

function DaemonRow({ daemon }: { daemon: AgentDaemon }) {
  return (
    <div className="rounded-md border border-slate-200 bg-white px-3 py-3">
      <div className="flex items-start justify-between gap-2">
        <p className="min-w-0 break-words text-sm font-semibold text-zinc-950">
          {daemon.name || daemon.id || "Unnamed agent"}
        </p>
        <span className="inline-flex shrink-0 items-center gap-1.5 rounded-md bg-emerald-50 px-2 py-1 text-[0.7rem] font-semibold uppercase tracking-normal text-emerald-700">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
          online
        </span>
      </div>
      <p className="mt-1 break-all font-mono text-xs text-slate-500">
        {daemon.runtime || "runtime"} {daemon.version || ""}
      </p>
    </div>
  );
}

function ConversationStatusPill({ status }: { status?: string }) {
  const value = status || "active";
  const tone =
    value === "active"
      ? "bg-emerald-50 text-emerald-700"
      : value === "idle"
        ? "bg-slate-100 text-slate-600"
        : value === "error" || value === "failed"
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

function formatRelativeTime(iso: string) {
  const then = Date.parse(iso);
  if (!Number.isFinite(then)) {
    return "";
  }
  const seconds = Math.round((Date.now() - then) / 1000);
  if (seconds < 45) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 7) return `${days}d ago`;
  // For older items fall back to a short calendar date.
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
