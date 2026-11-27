import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { canHostConversations } from "../../lib/daemons";
import { useMediaQuery } from "../../lib/useMediaQuery";
import {
  agentDaemonsQuery,
  allConversationsKey,
  recentConversationsKey,
  sliceConversationsQuery
} from "../../queries/agents";
import { AgentConversation } from "./AgentConversation";
import { AgentsSidebar, sortConversationsNewestFirst } from "./AgentsSidebar";
import { CreateConversationDialog } from "./CreateConversationDialog";
import { SliceNotice, SlicePanel, getErrorMessage } from "./SlicePageParts";

interface AgentsTabProps {
  api: ApiClient;
  conversationId?: string;
  onSelectConversation?: (id: string) => void;
  slice: SliceRef;
}

export function AgentsTab({
  api,
  conversationId,
  onSelectConversation,
  slice,
}: AgentsTabProps) {
  const queryClient = useQueryClient();
  const sliceKey = `${slice.account ?? ""}:${slice.slice ?? ""}`;
  const sliceDefined = Boolean(slice.account && slice.slice);

  const daemonsQuery = useQuery({
    ...agentDaemonsQuery(api),
    enabled: sliceDefined,
  });

  const conversationsQuery = useQuery({
    ...sliceConversationsQuery(api, slice),
    enabled: sliceDefined,
  });

  const daemons = daemonsQuery.data ?? [];
  const onlineDaemons = useMemo(
    () => daemons.filter((daemon) => daemon.status === "online" && canHostConversations(daemon)),
    [daemons],
  );
  const conversations = useMemo(
    () => sortConversationsNewestFirst(conversationsQuery.data ?? []),
    [conversationsQuery.data],
  );

  const [selectedConversationId, setSelectedConversationId] = useState("");
  const [selectedDaemonId, setSelectedDaemonId] = useState("");
  const [title, setTitle] = useState("");
  const [isSidebarOpen, setIsSidebarOpen] = useState(true);
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  // Tracks the lg breakpoint so we only auto-open the newest conversation on
  // desktop. On mobile the list is a full-screen view of its own, so
  // auto-opening would trap the user in a transcript with no way back to the
  // list. Defaults to false (also the SSR value) and resolves on mount.
  const isDesktop = useMediaQuery("(min-width: 1024px)");

  const selectConversation = useCallback(
    (id: string) => {
      setSelectedConversationId(id);
      onSelectConversation?.(id);
    },
    [onSelectConversation],
  );

  // Keep daemon selection valid as the online set changes. We only reassign
  // when the current pick is gone; we never clobber a user-chosen daemon.
  useEffect(() => {
    setSelectedDaemonId((current) =>
      onlineDaemons.some((daemon) => daemon.id === current)
        ? current
        : (onlineDaemons[0]?.id ?? ""),
    );
  }, [onlineDaemons]);

  useEffect(() => {
    if (conversationId === undefined) {
      return;
    }
    setSelectedConversationId((current) =>
      current === conversationId ? current : conversationId,
    );
  }, [conversationId]);

  // Default-select the newest conversation, but never overwrite an active
  // selection that still exists in the list. This runs when there is no
  // conversation in the URL (uncontrolled, or controlled empty on desktop).
  useEffect(() => {
    if (conversationId || (conversationId === "" && !isDesktop) || conversationsQuery.isPending) {
      return;
    }
    if (selectedConversationId && conversations.some((c) => c.id === selectedConversationId)) {
      return;
    }
    const next = conversations[0]?.id ?? "";
    if (next === selectedConversationId) {
      return;
    }
    setSelectedConversationId(next);
    if (next) {
      onSelectConversation?.(next);
    }
  }, [
    conversations,
    conversationsQuery.isPending,
    conversationId,
    isDesktop,
    onSelectConversation,
    selectedConversationId,
  ]);

  const selectedConversation = conversations.find(
    (conversation) => conversation.id === selectedConversationId,
  );

  const createMutation = useMutation({
    mutationFn: (input: { daemonId: string; title: string }) =>
      api.createConversation({
        daemonId: input.daemonId,
        slice,
        title: input.title || undefined,
      }),
    onSuccess: (conversation) => {
      queryClient.setQueryData(
        sliceConversationsQuery(api, slice).queryKey,
        (current) => [
          conversation,
          ...(current ?? []).filter((item) => item.id !== conversation.id),
        ],
      );
      void queryClient.invalidateQueries({ queryKey: recentConversationsKey });
      void queryClient.invalidateQueries({ queryKey: allConversationsKey });
      selectConversation(conversation.id ?? "");
      setTitle("");
      setIsCreateOpen(false);
      setIsSidebarOpen(true);
    },
  });

  function createConversation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedDaemonId) {
      return;
    }
    createMutation.mutate({ daemonId: selectedDaemonId, title: title.trim() });
  }

  function openCreate() {
    createMutation.reset();
    setIsCreateOpen(true);
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
            <div className="h-8 animate-pulse rounded-md bg-slate-200 dark:bg-zinc-700" />
            <div className="h-20 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            <div className="h-20 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
          </div>
          <div className="h-72 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
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

  const desktopSidebarToggle = (
    <button
      aria-controls="agent-conversation-sidebar"
      aria-expanded={isSidebarOpen}
      className="hidden rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50 active:scale-[0.98] lg:inline-flex"
      onClick={() => setIsSidebarOpen((open) => !open)}
      type="button"
    >
      {isSidebarOpen ? "Hide conversations" : "Show conversations"}
    </button>
  );

  const createError = createMutation.error ? getErrorMessage(createMutation.error) : "";
  const isCreating = createMutation.isPending;
  const hasSelectedConversation = Boolean(selectedConversationId);

  return (
    <SlicePanel className="p-0">
      <div
        className={cn(
          "grid gap-0",
          isSidebarOpen
            ? "lg:grid-cols-[20rem_minmax(0,1fr)]"
            : "lg:grid-cols-[minmax(0,1fr)]",
        )}
      >
        <AgentsSidebar
          conversations={conversations}
          hasSelectedConversation={hasSelectedConversation}
          isCreating={isCreating}
          isSidebarOpen={isSidebarOpen}
          onOpenCreate={openCreate}
          onSelectConversation={selectConversation}
          onlineDaemons={onlineDaemons}
          selectedConversationId={selectedConversationId}
          sliceKey={sliceKey}
        />

        <div
          className={cn(
            "min-h-0 min-w-0",
            hasSelectedConversation ? "block" : "hidden lg:block",
          )}
        >
          {selectedConversationId ? (
            <AgentConversation
              api={api}
              conversation={selectedConversation}
              conversationId={selectedConversationId}
              toolbar={<div className="flex items-center gap-2">{desktopSidebarToggle}</div>}
            />
          ) : (
            <div className="flex min-h-[60vh] flex-col bg-slate-50/60 dark:bg-zinc-950/60">
              <div className="border-b border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-3 sm:px-5">
                <div className="flex items-center justify-between gap-2">
                  <h2 className="truncate text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                    No conversation selected
                  </h2>
                  {desktopSidebarToggle}
                </div>
              </div>
              <div className="flex flex-1 items-center justify-center p-6 text-center">
                <div className="max-w-sm">
                  <p className="text-sm leading-6 text-slate-600 dark:text-zinc-400">
                    {onlineDaemons.length
                      ? "Start a new conversation with an online daemon, or pick one from the sidebar."
                      : "Run gs agent start to make an agent available, then create a conversation."}
                  </p>
                  {onlineDaemons.length ? (
                    <button
                      className="mt-4 rounded-md bg-zinc-950 dark:bg-zinc-100 px-4 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98]"
                      onClick={openCreate}
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

      <CreateConversationDialog
        createError={createError}
        isCreating={isCreating}
        onClose={() => setIsCreateOpen(false)}
        onSelectDaemon={setSelectedDaemonId}
        onSubmit={createConversation}
        onTitleChange={setTitle}
        onlineDaemons={onlineDaemons}
        open={isCreateOpen}
        selectedDaemonId={selectedDaemonId}
        sliceKey={sliceKey}
        title={title}
      />
    </SlicePanel>
  );
}
