import { useMemo } from "react";

import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";

import type { ConversationEvent } from "../../api/types";
import { useApi } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { renderMarkdownToHtml } from "../../lib/markdown";
import { useInternalLinkClickHandler } from "../../lib/internalLinkNavigation";

import {
  traceGroupLabel,
  tracePreview,
  eventKey,
  traceEventLabel,
  hasSequence,
  traceEventContent,
  isMessageDelta,
  parseCapturedPatchset
} from "./conversationEvents";

export function ConversationStatusDot({ status }: { status?: string }) {
  const value = status || "active";
  const active = value === "active";
  return (
    <span
      aria-label={value}
      className={cn(
        "inline-block h-2 w-2 shrink-0 rounded-full",
        active ? "bg-emerald-500" : "bg-slate-300 dark:bg-zinc-600"
      )}
      role="img"
      title={value}
    />
  );
}

export function ConversationTraceGroup({ events }: { events: ConversationEvent[] }) {
  return (
    <details className="mr-auto max-w-full rounded-md border border-slate-200 dark:border-zinc-800 bg-white/90 dark:bg-zinc-900/90 text-sm shadow-sm shadow-slate-200/60 dark:shadow-black/60 sm:max-w-[44rem]">
      <summary className="flex cursor-pointer items-start justify-between gap-3 px-3 py-2 text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950">
        <span className="min-w-0">
          <span className="block truncate font-semibold">
            {traceGroupLabel(events)}
          </span>
          {tracePreview(events) ? (
            <span className="mt-0.5 block truncate font-mono text-xs text-slate-500 dark:text-zinc-400">
              {tracePreview(events)}
            </span>
          ) : null}
        </span>
        <span className="shrink-0 rounded bg-slate-100 dark:bg-zinc-800 px-2 py-0.5 text-xs font-semibold text-slate-500 dark:text-zinc-400">
          details
        </span>
      </summary>
      <div className="grid grid-cols-1 gap-2 border-t border-slate-100 dark:border-zinc-800 bg-slate-50/70 dark:bg-zinc-950/70 p-3">
        {events.map((event, index) => (
          <div
            className="rounded-md bg-white dark:bg-zinc-900 p-2 shadow-sm shadow-slate-200/50 dark:shadow-black/50"
            key={eventKey(event, index)}
          >
            <div className="mb-1 flex flex-wrap items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
              <span>{traceEventLabel(event)}</span>
              {hasSequence(event) ? (
                <span className="text-slate-400 dark:text-zinc-500">#{event.seq}</span>
              ) : null}
            </div>
            {traceEventContent(event) ? (
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-xs leading-5 text-slate-700 dark:text-zinc-300">
                {traceEventContent(event)}
              </pre>
            ) : (
              <p className="text-xs text-slate-500 dark:text-zinc-400">No payload</p>
            )}
          </div>
        ))}
      </div>
    </details>
  );
}

// MessageMarkdown renders an agent message as sanitized markdown. Parsing is
// synchronous so a streaming message re-renders cleanly on every token. The
// prose classes are tuned for chat bubbles: tight vertical rhythm and no
// leading/trailing margin so the rendered block sits flush in the bubble.
function MessageMarkdown({ source }: { source: string }) {
  const html = useMemo(() => renderMarkdownToHtml(source), [source]);
  const onLinkClick = useInternalLinkClickHandler();
  return (
    <div
      className="prose prose-sm prose-slate dark:prose-invert max-w-none break-words prose-p:my-2 prose-pre:my-2 prose-pre:border prose-pre:border-slate-200 dark:prose-pre:border-zinc-800 prose-pre:bg-slate-50 dark:prose-pre:bg-zinc-950 prose-pre:text-zinc-900 dark:prose-pre:text-zinc-100 prose-code:before:content-none prose-code:after:content-none prose-headings:mb-2 prose-headings:mt-3 first:prose-headings:mt-0 prose-ul:my-2 prose-ol:my-2 prose-li:my-0.5 prose-a:text-sky-700 dark:prose-a:text-sky-300 prose-a:underline [&>:first-child]:mt-0 [&>:last-child]:mb-0"
      dangerouslySetInnerHTML={{ __html: html }}
      onClick={onLinkClick}
    />
  );
}

// LiveDeltaBubble renders an assistant message token stream still in flight.
export function LiveDeltaBubble({ event }: { event: ConversationEvent }) {
  const text = event.text ?? "";
  return (
    <article className="mr-auto max-w-full rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-2 text-sm leading-6 text-zinc-950 dark:text-zinc-50 shadow-sm shadow-slate-200/60 dark:shadow-black/60 sm:max-w-[44rem]">
      <div className="mb-1 flex items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
        <span>agent</span>
      </div>
      <MessageMarkdown source={text} />
      <span className="mt-0.5 inline-block h-3.5 w-1.5 animate-pulse bg-zinc-400 align-baseline" />
    </article>
  );
}

// AgentWorkingIndicator is the animated "agent is busy" affordance shown at the
// tail of the transcript while a turn is in flight but no token stream is yet
// visible.
export function AgentWorkingIndicator({ label }: { label: string }) {
  return (
    <div
      aria-label="Agent activity"
      aria-live="polite"
      className="mr-auto flex w-fit items-center gap-2 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-3 py-2 text-sm text-slate-600 dark:text-zinc-400 shadow-sm shadow-slate-200/60 dark:shadow-black/60"
      role="status"
    >
      <span aria-hidden="true" className="flex gap-1">
        <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-slate-400 [animation-delay:-0.3s]" />
        <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-slate-400 [animation-delay:-0.15s]" />
        <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-slate-400" />
      </span>
      <span>{label}</span>
    </div>
  );
}

// TranscriptSkeleton fills the transcript region with placeholder bubbles while
// a conversation's history loads. It grows to fill the reserved height held
// during a conversation switch (flex-1) and anchors its bubbles to the bottom
// (justify-end), matching where the loaded transcript pins the scroll — so the
// swap from skeleton to real messages happens in place, without a visible jump.
export function TranscriptSkeleton() {
  return (
    <div
      aria-hidden
      className="flex flex-1 flex-col justify-end gap-3"
      role="presentation"
    >
      <span className="mr-auto h-16 w-full max-w-[32rem] animate-pulse rounded-md bg-slate-200/70 dark:bg-zinc-700/70" />
      <span className="ml-auto h-10 w-full max-w-[20rem] animate-pulse rounded-md bg-slate-200/70 dark:bg-zinc-700/70" />
      <span className="mr-auto h-24 w-full max-w-[36rem] animate-pulse rounded-md bg-slate-200/70 dark:bg-zinc-700/70" />
      <span className="ml-auto h-11 w-full max-w-[16rem] animate-pulse rounded-md bg-slate-200/70 dark:bg-zinc-700/70" />
      <span className="mr-auto h-20 w-full max-w-[34rem] animate-pulse rounded-md bg-slate-200/70 dark:bg-zinc-700/70" />
    </div>
  );
}

export function ConversationEventBubble({ event }: { event: ConversationEvent }) {
  const role = event.role || "system";
  const isUser = role === "user";
  const isAgent = role === "agent";
  const hideType = isAgent && isMessageDelta(event);
  const content = event.text || event.dataJson || event.type || "";
  const capturedPatchset = parseCapturedPatchset(event);

  return (
    <article
      className={cn(
        "rounded-md px-3 py-2 text-sm leading-6 shadow-sm",
        isUser &&
          "ml-auto max-w-[min(44rem,85%)] bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 shadow-slate-900/10",
        isAgent &&
          "mr-auto max-w-full border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-zinc-950 dark:text-zinc-50 shadow-slate-200/60 dark:shadow-black/60 sm:max-w-[44rem]",
        !isUser &&
          !isAgent &&
          "mx-auto max-w-full border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-700 dark:text-zinc-300 shadow-slate-200/60 dark:shadow-black/60 sm:max-w-[44rem]"
      )}
    >
      <div
        className={cn(
          "mb-1 flex items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal",
          isUser ? "text-slate-300 dark:text-zinc-500" : "text-slate-500 dark:text-zinc-400"
        )}
      >
        <span>{role}</span>
        {event.type && !hideType ? <span>{event.type}</span> : null}
      </div>
      {capturedPatchset ? (
        <div className="grid grid-cols-1 gap-2">
          <p className="font-medium text-zinc-950 dark:text-zinc-50">
            Captured patchset {capturedPatchset.patchsetNumber}
          </p>
          <CapturedChangesetLink changesetId={capturedPatchset.changesetId} />
        </div>
      ) : event.dataJson && !event.text ? (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-100 dark:bg-zinc-800 p-2 font-mono text-xs text-slate-700 dark:text-zinc-300">
          {event.dataJson}
        </pre>
      ) : isAgent && event.text ? (
        <MessageMarkdown source={content} />
      ) : (
        <p className="whitespace-pre-wrap break-words">{content}</p>
      )}
    </article>
  );
}

// Links to the changeset detail with a client-side transition (not a bare <a>,
// which would hard-reload the SPA — rebooting auth/session and refetching
// everything in both directions). Both routes share the same publicApp shell,
// so this swaps only the outlet and keeps the conversation in the query cache,
// making the return trip instant. Hovering warms the changeset query so the
// detail header renders without a round-trip on click.
function CapturedChangesetLink({ changesetId }: { changesetId: string }) {
  const api = useApi();
  const queryClient = useQueryClient();

  const prefetchChangeset = () => {
    void queryClient.prefetchQuery({
      queryKey: ["changeset", changesetId],
      queryFn: () => api.getChangeset({ changesetId })
    });
  };

  return (
    <Link
      className="inline-flex w-fit max-w-full items-center rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 font-mono text-xs font-semibold text-zinc-900 dark:text-zinc-100 underline decoration-slate-300 underline-offset-4 transition hover:border-slate-400 dark:hover:border-zinc-600 hover:decoration-slate-700 active:scale-[0.98]"
      onFocus={prefetchChangeset}
      onMouseEnter={prefetchChangeset}
      params={{ id: changesetId }}
      to="/cs/$id"
    >
      <span className="truncate">changeset {changesetId}</span>
    </Link>
  );
}
