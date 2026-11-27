import {
  FormEvent,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState
} from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { Conversation } from "../../api/types";
import { type ApiClient } from "../../api/useApi";
import {
  allConversationsKey,
  conversationKey,
  conversationQuery,
  recentConversationsKey,
  recentConversationsQuery,
  sliceConversationsKeyPrefix
} from "../../queries/agents";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/Button";
import { cn } from "../../lib/cn";
import { useCopyToClipboard } from "../../lib/useCopyToClipboard";
import { getErrorMessage } from "./SlicePageParts";
import {
  eventKey,
  traceGroupKey,
  updateConversationList
} from "./conversationEvents";
import {
  AgentWorkingIndicator,
  ConversationEventBubble,
  ConversationStatusDot,
  ConversationTraceGroup,
  LiveDeltaBubble,
  TranscriptSkeleton
} from "./ConversationParts";
import { useConversationTranscript } from "./useConversationTranscript";

interface AgentConversationProps {
  api: ApiClient;
  conversation?: Conversation;
  conversationId: string;
  readOnly?: boolean;
  toolbar?: ReactNode;
}

export function AgentConversation({
  api,
  conversation,
  conversationId,
  readOnly = false,
  toolbar
}: AgentConversationProps) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState("");
  const [sendError, setSendError] = useState("");
  const [isSending, setIsSending] = useState(false);
  const [isCloseConfirmOpen, setIsCloseConfirmOpen] = useState(false);
  const {
    announcement: copyAnnouncement,
    copied: linkCopied,
    copy: copyToClipboard,
    failed: linkCopyFailed
  } = useCopyToClipboard({
    resetMs: 1500,
    copiedMessage: "Link copied",
    failedMessage: "Copy failed"
  });
  const messagesRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);

  const title = useMemo(
    () => conversation?.title || conversationId,
    [conversation?.title, conversationId]
  );
  const agentOffline = conversation?.daemonOnline === false;
  const conversationStatus = conversation?.status || "active";
  const isConversationActive = conversationStatus === "active";

  const {
    events,
    liveDeltas,
    conversationItems,
    agentWorkingLabel,
    historyLoaded,
    reservedMinHeight,
    hasMoreHistory,
    isLoadingEarlier,
    earlierError,
    loadEarlierMessages,
    streamError,
    isStreamReconnecting,
    reconnectNow,
    appendSentEvent,
    stickToBottom
  } = useConversationTranscript({
    api,
    conversationId,
    agentOffline,
    messagesRef,
    topSentinelRef
  });

  useEffect(() => {
    setSendError("");
    setDraft("");
  }, [conversationId]);

  const closeConversationMutation = useMutation({
    mutationFn: () => api.closeConversation({ conversationId }),
    onSuccess: (updatedConversation) => {
      queryClient.setQueryData(
        conversationQuery(api, conversationId).queryKey,
        updatedConversation
      );
      queryClient.setQueriesData<Conversation[]>(
        { queryKey: sliceConversationsKeyPrefix },
        (current) =>
          updateConversationList(current, conversationId, updatedConversation)
      );
      queryClient.setQueryData(
        recentConversationsQuery(api).queryKey,
        (current) =>
          updateConversationList(current, conversationId, updatedConversation)
      );
      void queryClient.invalidateQueries({
        queryKey: conversationKey(conversationId)
      });
      void queryClient.invalidateQueries({ queryKey: sliceConversationsKeyPrefix });
      void queryClient.invalidateQueries({ queryKey: recentConversationsKey });
      void queryClient.invalidateQueries({ queryKey: allConversationsKey });
      setIsCloseConfirmOpen(false);
    }
  });

  async function copyConversationLink() {
    if (typeof window === "undefined") {
      return;
    }
    await copyToClipboard(window.location.href);
  }

  function closeConversation() {
    if (closeConversationMutation.isPending) {
      return;
    }
    closeConversationMutation.mutate();
  }

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (agentOffline) {
      return;
    }
    const text = draft.trim();
    if (!text || isSending) {
      return;
    }

    setIsSending(true);
    setSendError("");
    stickToBottom();
    try {
      const response = await api.sendAgentMessage({ conversationId, text });
      const sentEvent = response.event;
      if (sentEvent) {
        appendSentEvent(sentEvent);
      }
      setDraft("");
    } catch (error) {
      setSendError(getErrorMessage(error));
    } finally {
      setIsSending(false);
    }
  }

  return (
    <div className="flex min-h-[calc(100dvh-12rem)] flex-col">
      <div className="flex flex-1 flex-col bg-slate-50/60 dark:bg-zinc-950/60">
        <div className="border-b border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-3 sm:px-5">
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <h2 className="truncate text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                {title}
              </h2>
              {conversation?.workspaceSubdir ? (
                <p className="truncate font-mono text-xs text-slate-500 dark:text-zinc-400">
                  {conversation.workspaceSubdir}
                </p>
              ) : null}
            </div>
            <div className="flex shrink-0 items-center gap-2">
              {toolbar}
              <button
                aria-label="Copy conversation link"
                className={cn(
                  "inline-flex size-7 shrink-0 items-center justify-center rounded-md transition hover:bg-slate-100 dark:hover:bg-zinc-800 active:scale-[0.98]",
                  linkCopied
                    ? "text-emerald-600 dark:text-emerald-400"
                    : linkCopyFailed
                      ? "text-rose-600 dark:text-rose-400"
                      : "text-slate-400 dark:text-zinc-500 hover:text-zinc-950 dark:hover:text-zinc-50"
                )}
                onClick={() => {
                  void copyConversationLink();
                }}
                title={linkCopied ? "Link copied" : linkCopyFailed ? "Copy failed" : "Copy link"}
                type="button"
              >
                <svg
                  aria-hidden="true"
                  className="size-4"
                  fill="none"
                  stroke="currentColor"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={1.8}
                  viewBox="0 0 24 24"
                >
                  {linkCopied ? (
                    <path d="M20 6 9 17l-5-5" />
                  ) : (
                    <>
                      <path d="M10 13a5 5 0 0 0 7.07 0l2.83-2.83a5 5 0 0 0-7.07-7.07L11.5 4.5" />
                      <path d="M14 11a5 5 0 0 0-7.07 0L4.1 13.83a5 5 0 0 0 7.07 7.07l1.32-1.32" />
                    </>
                  )}
                </svg>
              </button>
              <span aria-live="polite" className="sr-only" role="status">
                {copyAnnouncement}
              </span>
              {!readOnly && isConversationActive ? (
                <Button
                  className="font-semibold hover:border-slate-300 dark:hover:border-zinc-700"
                  disabled={closeConversationMutation.isPending}
                  onClick={() => setIsCloseConfirmOpen(true)}
                  size="sm"
                  variant="secondary"
                >
                  {closeConversationMutation.isPending ? "Closing..." : "Close"}
                </Button>
              ) : null}
              <ConversationStatusDot status={conversationStatus} />
            </div>
          </div>
          {streamError ? (
            <div
              aria-label="Conversation stream status"
              aria-live="polite"
              className="mt-3 flex flex-col gap-2 rounded-md border border-amber-200 dark:border-amber-900/60 bg-amber-50 dark:bg-amber-950/30 px-3 py-2 text-xs text-amber-950 dark:text-amber-100 sm:flex-row sm:items-center sm:justify-between"
              role="status"
            >
              <div className="min-w-0">
                <p className="font-semibold">
                  {isStreamReconnecting
                    ? "Stream stopped; reconnecting"
                    : "Stream stopped"}
                </p>
                <p
                  className="mt-0.5 truncate leading-5 text-amber-900/80 dark:text-amber-200/80"
                  title={streamError}
                >
                  {streamError}
                </p>
              </div>
              <button
                className="w-fit rounded-md border border-amber-300 dark:border-amber-800/70 bg-white dark:bg-zinc-900 px-3 py-1.5 font-semibold text-amber-900 dark:text-amber-200 transition hover:bg-amber-100 dark:hover:bg-amber-950/45 active:scale-[0.98]"
                onClick={reconnectNow}
                type="button"
              >
                Reconnect now
              </button>
            </div>
          ) : null}
        </div>

        <div
          className="flex min-h-64 flex-1 flex-col px-3 py-4 sm:px-5"
          ref={messagesRef}
          style={
            reservedMinHeight != null
              ? { minHeight: reservedMinHeight }
              : undefined
          }
        >
          {events.length === 0 && liveDeltas.size === 0 ? (
            !historyLoaded ? (
              <TranscriptSkeleton />
            ) : (
              <div className="flex min-h-64 items-center justify-center text-center">
                <div className="max-w-sm">
                  <h3 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                    No messages yet
                  </h3>
                  <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                    Send a message to start this agent conversation.
                  </p>
                </div>
              </div>
            )
          ) : (
            <div className="mt-auto grid grid-cols-1 gap-3">
              {hasMoreHistory || isLoadingEarlier || earlierError ? (
                <div
                  className="flex flex-col items-center gap-1 pb-1"
                  ref={topSentinelRef}
                >
                  <button
                    className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-1 text-xs font-semibold text-slate-600 dark:text-zinc-400 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50 disabled:cursor-not-allowed disabled:opacity-60"
                    disabled={isLoadingEarlier}
                    onClick={() => {
                      void loadEarlierMessages();
                    }}
                    type="button"
                  >
                    {isLoadingEarlier
                      ? "Loading earlier messages…"
                      : "Load earlier messages"}
                  </button>
                  {earlierError ? (
                    <p className="text-xs text-rose-700 dark:text-rose-300">{earlierError}</p>
                  ) : null}
                </div>
              ) : null}
              {conversationItems.map((item, index) =>
                item.kind === "trace" ? (
                  <ConversationTraceGroup
                    events={item.events}
                    key={traceGroupKey(item.events, index)}
                  />
                ) : item.kind === "live" ? (
                  <LiveDeltaBubble
                    event={item.entry.event}
                    key={`live:${item.entry.event.itemId}`}
                  />
                ) : (
                  <ConversationEventBubble
                    event={item.event}
                    key={eventKey(item.event, index)}
                  />
                )
              )}
              {agentWorkingLabel ? (
                <AgentWorkingIndicator label={agentWorkingLabel} />
              ) : null}
            </div>
          )}
        </div>
      </div>

      {readOnly ? null : (
        <form
          className="sticky bottom-0 z-20 border-t border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-3 sm:px-5"
          onSubmit={sendMessage}
        >
          {agentOffline ? (
            <div className="mb-3 rounded-md border border-amber-300 dark:border-amber-800/70 bg-amber-50 dark:bg-amber-950/30 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">
              This agent is offline. Run{" "}
              <code className="rounded bg-white dark:bg-zinc-900 px-1.5 py-0.5 font-mono text-xs text-amber-900 dark:text-amber-200">
                gs agent start
              </code>{" "}
              on the machine hosting it to resume the conversation.
            </div>
          ) : null}
          <div className="flex items-end gap-2">
            <textarea
              aria-label="Message"
              className="max-h-48 min-h-16 w-full min-w-0 resize-y rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm leading-6 text-zinc-950 dark:text-zinc-50 outline-none transition placeholder:text-slate-400 dark:placeholder:text-zinc-500 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 dark:focus:ring-zinc-700 disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-500 dark:disabled:text-zinc-400"
              disabled={agentOffline}
              onChange={(event) => setDraft(event.target.value)}
              placeholder={
                agentOffline
                  ? "Agent is offline - start the daemon to continue"
                  : "Ask the agent to inspect or edit this slice"
              }
              value={draft}
            />
            <button
              className="shrink-0 rounded-md bg-zinc-950 dark:bg-zinc-100 px-4 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-zinc-600"
              disabled={isSending || !draft.trim() || agentOffline}
              type="submit"
            >
              {isSending ? "Sending..." : "Send"}
            </button>
          </div>
          {sendError ? (
            <p className="mt-2 text-sm text-rose-700 dark:text-rose-300">{sendError}</p>
          ) : null}
        </form>
      )}
      <ConfirmDialog
        busy={closeConversationMutation.isPending}
        confirmLabel="Close"
        message="Closing deletes the conversation workspace on the agent. The transcript will stay available for review."
        onCancel={() => setIsCloseConfirmOpen(false)}
        onConfirm={closeConversation}
        open={isCloseConfirmOpen}
        title="Close conversation"
        tone="danger"
      />
    </div>
  );
}
