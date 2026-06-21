import {
  FormEvent,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState
} from "react";

import type { Conversation, ConversationEvent } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { getErrorMessage } from "./SlicePageParts";

interface AgentConversationProps {
  api: ApiClient;
  conversation?: Conversation;
  conversationId: string;
  toolbar?: ReactNode;
}

export function AgentConversation({
  api,
  conversation,
  conversationId,
  toolbar
}: AgentConversationProps) {
  const [events, setEvents] = useState<ConversationEvent[]>([]);
  const [draft, setDraft] = useState("");
  const [sendError, setSendError] = useState("");
  const [streamError, setStreamError] = useState("");
  const [isSending, setIsSending] = useState(false);
  // Bumping retryKey re-runs the stream effect after a stream error, without
  // needing to remount the component or change conversationId.
  const [retryKey, setRetryKey] = useState(0);
  const endRef = useRef<HTMLDivElement>(null);
  const scrollContainerRef = useRef<HTMLDivElement>(null);
  // Tracked as a ref so scroll position changes don't trigger re-renders; the
  // scroll-on-new-event effect reads the latest value at effect time.
  const stickToBottomRef = useRef(true);

  const title = useMemo(
    () => conversation?.title || conversationId,
    [conversation?.title, conversationId]
  );
  const conversationItems = useMemo(
    () => groupConversationEvents(events),
    [events]
  );

  // Reset transcript state only when the conversation actually changes. This is
  // deliberately separate from the stream effect below so that a Reconnect
  // (which bumps retryKey) re-attaches the stream without wiping the user's
  // in-progress draft or the partial transcript already on screen.
  useEffect(() => {
    setEvents([]);
    setStreamError("");
    setSendError("");
    setDraft("");
    // When switching conversations, treat the new view as "stick to bottom".
    stickToBottomRef.current = true;
  }, [conversationId]);

  useEffect(() => {
    const controller = new AbortController();

    async function readStream() {
      try {
        for await (const event of api.streamConversation(
          { conversationId, afterSeq: 0 },
          controller.signal
        )) {
          if (controller.signal.aborted) {
            return;
          }
          setEvents((current) => appendConversationEvent(current, event));
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setStreamError(getErrorMessage(error));
        }
      }
    }

    void readStream();

    return () => {
      controller.abort();
    };
  }, [api, conversationId, retryKey]);

  // Auto-scroll on new events only when the user is already parked at the
  // bottom; otherwise typing/scrolling up to read history would be yanked
  // away on every streamed token.
  useEffect(() => {
    if (!stickToBottomRef.current) {
      return;
    }
    endRef.current?.scrollIntoView?.({ block: "end" });
  }, [events.length]);

  function handleScroll() {
    const el = scrollContainerRef.current;
    if (!el) {
      return;
    }
    // 96px slop so minor scrollback / scrollbar rounding doesn't detach.
    const atBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < 96;
    stickToBottomRef.current = atBottom;
  }

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const text = draft.trim();
    if (!text || isSending) {
      return;
    }

    setIsSending(true);
    setSendError("");
    try {
      const response = await api.sendAgentMessage({ conversationId, text });
      const sentEvent = response.event;
      if (sentEvent) {
        setEvents((current) =>
          appendConversationEvent(current, sentEvent)
        );
      }
      setDraft("");
    } catch (error) {
      setSendError(getErrorMessage(error));
    } finally {
      setIsSending(false);
    }
  }

  return (
    <div className="flex h-full min-h-[26rem] flex-col">
      <div className="border-b border-slate-200 px-4 py-4 sm:px-5">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <h2 className="truncate text-base font-semibold text-zinc-950">
              {title}
            </h2>
            {conversation?.workspaceSubdir ? (
              <p className="mt-1 truncate font-mono text-xs text-slate-500">
                {conversation.workspaceSubdir}
              </p>
            ) : null}
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {toolbar}
            <span className="inline-flex w-fit rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-600">
              {conversation?.status || "active"}
            </span>
          </div>
        </div>
      </div>

      <div
        className="min-h-0 flex-1 overflow-y-auto bg-slate-50/60 px-4 py-4 sm:px-5"
        onScroll={handleScroll}
        ref={scrollContainerRef}
      >
        {streamError ? (
          <div className="mb-4 rounded-md border border-rose-200 bg-rose-50 p-3 text-sm text-rose-900">
            <p className="font-semibold">Stream stopped</p>
            <p className="mt-1 leading-6">{streamError}</p>
            <button
              className="mt-2 rounded-md border border-rose-300 bg-white px-3 py-1.5 text-xs font-semibold text-rose-800 transition hover:bg-rose-100 active:scale-[0.98]"
              onClick={() => {
                setStreamError("");
                setRetryKey((value) => value + 1);
              }}
              type="button"
            >
              Reconnect
            </button>
          </div>
        ) : null}

        {events.length === 0 ? (
          <div className="flex min-h-64 items-center justify-center text-center">
            <div className="max-w-sm">
              <h3 className="text-sm font-semibold text-zinc-950">
                No messages yet
              </h3>
              <p className="mt-2 text-sm leading-6 text-slate-600">
                Send a message to start this agent conversation.
              </p>
            </div>
          </div>
        ) : (
          <div className="grid gap-3">
            {conversationItems.map((item, index) =>
              item.kind === "trace" ? (
                <ConversationTraceGroup
                  events={item.events}
                  key={traceGroupKey(item.events, index)}
                />
              ) : (
                <ConversationEventBubble
                  event={item.event}
                  key={eventKey(item.event, index)}
                />
              )
            )}
          </div>
        )}
        <div ref={endRef} />
      </div>

      <form
        className="border-t border-slate-200 bg-white px-4 py-4 sm:px-5"
        onSubmit={sendMessage}
      >
        <label className="grid gap-2 text-sm font-medium text-zinc-800">
          Message
          <textarea
            className="max-h-48 min-h-24 w-full min-w-0 resize-y rounded-md border border-slate-300 bg-white px-3 py-2 text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Ask the agent to inspect or edit this slice"
            value={draft}
          />
        </label>
        {sendError ? (
          <p className="mt-2 text-sm text-rose-700">{sendError}</p>
        ) : null}
        <div className="mt-3 flex justify-end">
          <button
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
            disabled={isSending || !draft.trim()}
            type="submit"
          >
            {isSending ? "Sending..." : "Send"}
          </button>
        </div>
      </form>
    </div>
  );
}

type ConversationItem =
  | { kind: "event"; event: ConversationEvent }
  | { kind: "trace"; events: ConversationEvent[] };

function groupConversationEvents(events: ConversationEvent[]): ConversationItem[] {
  const items: ConversationItem[] = [];
  let traceEvents: ConversationEvent[] = [];

  function flushTraceEvents() {
    if (traceEvents.length) {
      items.push({ kind: "trace", events: traceEvents });
      traceEvents = [];
    }
  }

  for (const event of events) {
    if (isTraceEvent(event)) {
      traceEvents.push(event);
      continue;
    }

    flushTraceEvents();
    items.push({ kind: "event", event });
  }

  flushTraceEvents();
  return items;
}

function ConversationTraceGroup({ events }: { events: ConversationEvent[] }) {
  return (
    <details className="mr-auto max-w-[min(44rem,92%)] rounded-md border border-slate-200 bg-white/90 text-sm shadow-sm shadow-slate-200/60">
      <summary className="flex cursor-pointer items-start justify-between gap-3 px-3 py-2 text-slate-700 transition hover:bg-slate-50">
        <span className="min-w-0">
          <span className="block truncate font-semibold">
            {traceGroupLabel(events)}
          </span>
          {tracePreview(events) ? (
            <span className="mt-0.5 block truncate font-mono text-xs text-slate-500">
              {tracePreview(events)}
            </span>
          ) : null}
        </span>
        <span className="shrink-0 rounded bg-slate-100 px-2 py-0.5 text-xs font-semibold text-slate-500">
          details
        </span>
      </summary>
      <div className="grid gap-2 border-t border-slate-100 bg-slate-50/70 p-3">
        {events.map((event, index) => (
          <div
            className="rounded-md bg-white p-2 shadow-sm shadow-slate-200/50"
            key={eventKey(event, index)}
          >
            <div className="mb-1 flex flex-wrap items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal text-slate-500">
              <span>{traceEventLabel(event)}</span>
              {hasSequence(event) ? (
                <span className="text-slate-400">#{event.seq}</span>
              ) : null}
            </div>
            {traceEventContent(event) ? (
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-xs leading-5 text-slate-700">
                {traceEventContent(event)}
              </pre>
            ) : (
              <p className="text-xs text-slate-500">No payload</p>
            )}
          </div>
        ))}
      </div>
    </details>
  );
}

function ConversationEventBubble({ event }: { event: ConversationEvent }) {
  const role = event.role || "system";
  const isUser = role === "user";
  const isAgent = role === "agent";
  const content = event.text || event.dataJson || event.type || "";
  const capturedPatchset = parseCapturedPatchset(event);

  return (
    <article
      className={cn(
        "max-w-[min(44rem,85%)] rounded-md px-3 py-2 text-sm leading-6 shadow-sm",
        isUser && "ml-auto bg-zinc-950 text-white shadow-slate-900/10",
        isAgent &&
          "mr-auto border border-slate-200 bg-white text-zinc-950 shadow-slate-200/60",
        !isUser &&
          !isAgent &&
          "mx-auto border border-slate-200 bg-white text-slate-700 shadow-slate-200/60"
      )}
    >
      <div
        className={cn(
          "mb-1 flex items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal",
          isUser ? "text-slate-300" : "text-slate-500"
        )}
      >
        <span>{role}</span>
        {event.type ? <span>{event.type}</span> : null}
        {hasSequence(event) ? (
          <span className="text-slate-400">#{event.seq}</span>
        ) : null}
      </div>
      {capturedPatchset ? (
        <div className="grid gap-2">
          <p className="font-medium text-zinc-950">
            Captured patchset {capturedPatchset.patchsetNumber}
          </p>
          <a
            className="inline-flex w-fit max-w-full items-center rounded-md border border-slate-300 bg-white px-2.5 py-1.5 font-mono text-xs font-semibold text-zinc-900 underline decoration-slate-300 underline-offset-4 transition hover:border-slate-400 hover:decoration-slate-700 active:scale-[0.98]"
            href={`/cs/${encodeURIComponent(capturedPatchset.changesetId)}`}
          >
            <span className="truncate">
              changeset {capturedPatchset.changesetId}
            </span>
          </a>
        </div>
      ) : event.dataJson && !event.text ? (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-100 p-2 font-mono text-xs text-slate-700">
          {event.dataJson}
        </pre>
      ) : (
        <p className="whitespace-pre-wrap break-words">{content}</p>
      )}
    </article>
  );
}

function isTraceEvent(event: ConversationEvent) {
  const role = (event.role ?? "").toLowerCase();
  const type = (event.type ?? "").toLowerCase();
  return (
    role === "tool" ||
    type === "delta" ||
    type === "tool_call" ||
    type === "tool_output" ||
    type === "thinking" ||
    type === "thinking_trace" ||
    type === "reasoning" ||
    type === "reasoning_delta"
  );
}

function traceGroupLabel(events: ConversationEvent[]) {
  const hasToolEvent = events.some((event) => {
    const type = (event.type ?? "").toLowerCase();
    return type === "tool_call" || type === "tool_output";
  });
  const hasTraceEvent = events.some((event) => {
    const type = (event.type ?? "").toLowerCase();
    return (
      type === "delta" ||
      type === "thinking" ||
      type === "thinking_trace" ||
      type === "reasoning" ||
      type === "reasoning_delta"
    );
  });

  if (hasToolEvent && hasTraceEvent) {
    return `Trace and tools (${events.length})`;
  }
  if (hasToolEvent) {
    return `Tool activity (${events.length})`;
  }
  return `Agent trace (${events.length})`;
}

function tracePreview(events: ConversationEvent[]) {
  const firstContent = events
    .map(traceEventContent)
    .find((content) => content.trim() !== "");
  if (!firstContent) {
    return "";
  }
  return firstContent.trim().split(/\r?\n/, 1)[0];
}

function traceEventLabel(event: ConversationEvent) {
  const role = event.role || "agent";
  const type = event.type || "trace";
  return `${role} ${type}`;
}

function traceEventContent(event: ConversationEvent) {
  return event.text || event.dataJson || "";
}

function traceGroupKey(events: ConversationEvent[], index: number) {
  const first = events[0] ? eventIdentity(events[0]) : "";
  const last = events[events.length - 1]
    ? eventIdentity(events[events.length - 1])
    : "";
  return `trace:${first || index}:${last || events.length}`;
}

function appendConversationEvent(
  current: ConversationEvent[],
  event: ConversationEvent
) {
  const identity = eventIdentity(event);
  if (identity && current.some((item) => eventIdentity(item) === identity)) {
    return current;
  }

  const next = [...current, event];
  if (hasSequence(event)) {
    next.sort(compareConversationEvents);
  }
  return next;
}

function compareConversationEvents(
  left: ConversationEvent,
  right: ConversationEvent
) {
  const leftSeq = Number(left.seq);
  const rightSeq = Number(right.seq);
  if (Number.isFinite(leftSeq) && Number.isFinite(rightSeq)) {
    return leftSeq - rightSeq;
  }
  return 0;
}

function eventIdentity(event: ConversationEvent) {
  if (event.id) {
    return `id:${event.id}`;
  }
  if (event.conversationId && hasSequence(event)) {
    return `seq:${event.conversationId}:${event.seq}`;
  }
  return "";
}

function eventKey(event: ConversationEvent, index: number) {
  return eventIdentity(event) || `${event.role ?? "event"}-${index}`;
}

function hasSequence(event: ConversationEvent) {
  return event.seq !== undefined && event.seq !== "";
}

function parseCapturedPatchset(event: ConversationEvent) {
  if (event.role !== "system" || event.type !== "status" || !event.text) {
    return null;
  }

  const match = event.text
    .trim()
    .match(/^captured changeset ([^\s]+) patchset ([1-9][0-9]*)$/);
  if (!match) {
    return null;
  }

  return {
    changesetId: match[1],
    patchsetNumber: match[2]
  };
}
