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

const STREAM_RECONNECT_DELAY_MS = 1500;

interface AgentConversationProps {
  api: ApiClient;
  conversation?: Conversation;
  conversationId: string;
  // Rendered at the top of the scroll region, above the conversation header, so
  // it scrolls away with the rest of the header (e.g. the page breadcrumb).
  leading?: ReactNode;
  toolbar?: ReactNode;
}

export function AgentConversation({
  api,
  conversation,
  conversationId,
  leading,
  toolbar
}: AgentConversationProps) {
  const [events, setEvents] = useState<ConversationEvent[]>([]);
  // In-progress token streams, keyed by runtime item id. They are stored
  // separately from finalized events so the UI can coalesce streamed snapshots
  // until the matching finalized event arrives.
  const [liveDeltas, setLiveDeltas] = useState<Map<string, LiveDeltaEntry>>(
    () => new Map()
  );
  const [draft, setDraft] = useState("");
  const [sendError, setSendError] = useState("");
  const [streamError, setStreamError] = useState("");
  const [isStreamReconnecting, setIsStreamReconnecting] = useState(false);
  const [isSending, setIsSending] = useState(false);
  // Bumping retryKey re-runs the stream effect after a stream stop, without
  // needing to remount the component or change conversationId.
  const [retryKey, setRetryKey] = useState(0);
  const scrollContainerRef = useRef<HTMLDivElement>(null);
  const latestPersistedSeqRef = useRef(0);
  const liveDeltaOrderRef = useRef(0);
  // Tracked as a ref so scroll position changes don't trigger re-renders; the
  // scroll-on-new-event effect reads the latest value at effect time.
  const stickToBottomRef = useRef(true);

  const title = useMemo(
    () => conversation?.title || conversationId,
    [conversation?.title, conversationId]
  );
  const conversationItems = useMemo(
    () => groupConversationEvents(events, [...liveDeltas.values()]),
    [events, liveDeltas]
  );

  // Reset transcript state only when the conversation actually changes. This is
  // deliberately separate from the stream effect below so that a Reconnect
  // (which bumps retryKey) re-attaches the stream without wiping the user's
  // in-progress draft or the partial transcript already on screen.
  useEffect(() => {
    setEvents([]);
    setLiveDeltas(new Map());
    latestPersistedSeqRef.current = 0;
    liveDeltaOrderRef.current = 0;
    setStreamError("");
    setIsStreamReconnecting(false);
    setSendError("");
    setDraft("");
    // When switching conversations, treat the new view as "stick to bottom".
    stickToBottomRef.current = true;
  }, [conversationId]);

  useEffect(() => {
    const controller = new AbortController();
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    // Each stream attempt starts from seq 0, so drop any stale in-progress
    // deltas; persisted finals will re-arrive and any live tail re-streams.
    setLiveDeltas(new Map());
    setStreamError("");
    setIsStreamReconnecting(false);

    function scheduleReconnect(message: string) {
      if (controller.signal.aborted) {
        return;
      }
      setStreamError(message);
      setIsStreamReconnecting(true);
      reconnectTimer = setTimeout(() => {
        if (!controller.signal.aborted) {
          setRetryKey((value) => value + 1);
        }
      }, STREAM_RECONNECT_DELAY_MS);
    }

    async function readStream() {
      try {
        for await (const event of api.streamConversation(
          { conversationId, afterSeq: 0 },
          controller.signal
        )) {
          if (controller.signal.aborted) {
            return;
          }
          if (isLiveDelta(event)) {
            const afterSeq = latestPersistedSeqRef.current;
            const order = liveDeltaOrderRef.current++;
            setLiveDeltas((current) => {
              const next = new Map(current);
              const existing = current.get(event.itemId as string);
              next.set(event.itemId as string, {
                afterSeq: existing?.afterSeq ?? afterSeq,
                event,
                order: existing?.order ?? order
              });
              return next;
            });
            rememberPersistedEventSeq(event, latestPersistedSeqRef);
            continue;
          }
          rememberPersistedEventSeq(event, latestPersistedSeqRef);
          setEvents((current) => appendConversationEvent(current, event));
          // A finalized event supersedes its in-progress delta bubble.
          if (event.itemId) {
            setLiveDeltas((current) => {
              if (!current.has(event.itemId as string)) {
                return current;
              }
              const next = new Map(current);
              next.delete(event.itemId as string);
              return next;
            });
          }
        }
        if (!controller.signal.aborted) {
          scheduleReconnect("Stream closed before the conversation finished.");
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          scheduleReconnect(getErrorMessage(error));
        }
      }
    }

    void readStream();

    return () => {
      controller.abort();
      if (reconnectTimer !== undefined) {
        clearTimeout(reconnectTimer);
      }
    };
  }, [api, conversationId, retryKey]);

  // Auto-scroll on new events only when the user is already parked at the
  // bottom; otherwise typing/scrolling up to read history would be yanked
  // away on every streamed token. Scroll the container directly rather than
  // scrollIntoView() — the latter walks up to the nearest scrollable ancestor
  // (on mobile that's the document), which yanks the whole page around on
  // every streamed event.
  useEffect(() => {
    if (!stickToBottomRef.current) {
      return;
    }
    const el = scrollContainerRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
    }
    // `liveDeltas` changes identity on every streamed token, so this also keeps
    // the view pinned to the bottom as a message streams in.
  }, [events.length, liveDeltas]);

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
    // Sending is an explicit intent to follow the conversation, so re-stick to
    // the bottom even if the user had scrolled up to read history.
    stickToBottomRef.current = true;
    try {
      const response = await api.sendAgentMessage({ conversationId, text });
      const sentEvent = response.event;
      if (sentEvent) {
        rememberPersistedEventSeq(sentEvent, latestPersistedSeqRef);
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
    <div className="flex h-full min-h-0 flex-col">
      <div
        className="min-h-0 flex-1 overflow-y-auto bg-slate-50/60"
        onScroll={handleScroll}
        ref={scrollContainerRef}
      >
        {/* The header lives at the top of the scroll region rather than pinned
            above it, so it collapses out of view as you scroll the transcript —
            like the changeset detail page, whose header is normal page flow.
            Since the view is anchored to the latest message, the header starts
            scrolled away, handing its space to the conversation. */}
        <div className="border-b border-slate-200 bg-white px-3 py-3 sm:px-5">
          {leading ? <div className="mb-2">{leading}</div> : null}
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <h2 className="truncate text-sm font-semibold text-zinc-950">
                {title}
              </h2>
              {conversation?.workspaceSubdir ? (
                <p className="truncate font-mono text-xs text-slate-500">
                  {conversation.workspaceSubdir}
                </p>
              ) : null}
            </div>
            <div className="flex shrink-0 items-center gap-2">
              {toolbar}
              <span className="inline-flex w-fit rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-600">
                {conversation?.status || "active"}
              </span>
            </div>
          </div>
          {streamError ? (
            <div
              aria-label="Conversation stream status"
              aria-live="polite"
              className="mt-3 flex flex-col gap-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-950 sm:flex-row sm:items-center sm:justify-between"
              role="status"
            >
              <div className="min-w-0">
                <p className="font-semibold">
                  {isStreamReconnecting
                    ? "Stream stopped; reconnecting"
                    : "Stream stopped"}
                </p>
                <p
                  className="mt-0.5 truncate leading-5 text-amber-900/80"
                  title={streamError}
                >
                  {streamError}
                </p>
              </div>
              <button
                className="w-fit rounded-md border border-amber-300 bg-white px-3 py-1.5 font-semibold text-amber-900 transition hover:bg-amber-100 active:scale-[0.98]"
                onClick={() => {
                  setStreamError("");
                  setIsStreamReconnecting(false);
                  setRetryKey((value) => value + 1);
                }}
                type="button"
              >
                Reconnect now
              </button>
            </div>
          ) : null}
        </div>

        <div className="px-3 py-4 sm:px-5">
          {events.length === 0 && liveDeltas.size === 0 ? (
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
            <div className="grid grid-cols-1 gap-3">
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
            </div>
          )}
        </div>
      </div>

      <form
        className="border-t border-slate-200 bg-white px-3 py-3 sm:px-5"
        onSubmit={sendMessage}
      >
        <div className="flex items-end gap-2">
          <textarea
            aria-label="Message"
            className="max-h-48 min-h-16 w-full min-w-0 resize-y rounded-md border border-slate-300 bg-white px-3 py-2 text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200"
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Ask the agent to inspect or edit this slice"
            value={draft}
          />
          <button
            className="shrink-0 rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
            disabled={isSending || !draft.trim()}
            type="submit"
          >
            {isSending ? "Sending..." : "Send"}
          </button>
        </div>
        {sendError ? (
          <p className="mt-2 text-sm text-rose-700">{sendError}</p>
        ) : null}
      </form>
    </div>
  );
}

type ConversationItem =
  | { kind: "event"; event: ConversationEvent }
  | { kind: "trace"; events: ConversationEvent[] }
  | { kind: "live"; entry: LiveDeltaEntry };

interface LiveDeltaEntry {
  afterSeq: number;
  event: ConversationEvent;
  order: number;
}

function groupConversationEvents(
  events: ConversationEvent[],
  liveDeltas: LiveDeltaEntry[]
): ConversationItem[] {
  const items: ConversationItem[] = [];
  let traceEvents: ConversationEvent[] = [];
  const liveEntries = [...liveDeltas].sort(compareLiveDeltaEntries);
  let liveIndex = 0;

  function flushTraceEvents() {
    if (traceEvents.length) {
      items.push({ kind: "trace", events: traceEvents });
      traceEvents = [];
    }
  }

  function flushLiveDeltasBefore(seq: number) {
    while (
      liveIndex < liveEntries.length &&
      liveEntries[liveIndex].afterSeq < seq
    ) {
      flushTraceEvents();
      items.push({ kind: "live", entry: liveEntries[liveIndex] });
      liveIndex += 1;
    }
  }

  for (const event of events) {
    const seq = eventSequenceNumber(event);
    if (seq !== undefined) {
      flushLiveDeltasBefore(seq);
    }

    if (isTraceEvent(event)) {
      traceEvents.push(event);
      continue;
    }

    flushTraceEvents();
    items.push({ kind: "event", event });
  }

  flushTraceEvents();
  while (liveIndex < liveEntries.length) {
    items.push({ kind: "live", entry: liveEntries[liveIndex] });
    liveIndex += 1;
  }
  return items;
}

function ConversationTraceGroup({ events }: { events: ConversationEvent[] }) {
  return (
    <details className="mr-auto max-w-full rounded-md border border-slate-200 bg-white/90 text-sm shadow-sm shadow-slate-200/60 sm:max-w-[44rem]">
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
      <div className="grid grid-cols-1 gap-2 border-t border-slate-100 bg-slate-50/70 p-3">
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

// isLiveDelta detects an in-progress assistant message stream. Reasoning and
// tool trace events stay in the normal event list so they render in the
// collapsed trace groups by default.
function isLiveDelta(event: ConversationEvent): event is ConversationEvent & {
  itemId: string;
} {
  return Boolean(event.itemId) && event.type === "message_delta";
}

// LiveDeltaBubble renders an assistant message token stream still in flight.
function LiveDeltaBubble({ event }: { event: ConversationEvent }) {
  const text = event.text ?? "";
  return (
    <article className="mr-auto max-w-full rounded-md border border-slate-200 bg-white px-3 py-2 text-sm leading-6 text-zinc-950 shadow-sm shadow-slate-200/60 sm:max-w-[44rem]">
      <div className="mb-1 flex items-center gap-2 text-[0.7rem] font-semibold uppercase tracking-normal text-slate-500">
        <span>agent</span>
      </div>
      <p className="whitespace-pre-wrap break-words">
        {text}
        <span className="ml-0.5 inline-block h-3.5 w-1.5 translate-y-0.5 animate-pulse bg-zinc-400 align-baseline" />
      </p>
    </article>
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
        "rounded-md px-3 py-2 text-sm leading-6 shadow-sm",
        isUser &&
          "ml-auto max-w-[min(44rem,85%)] bg-zinc-950 text-white shadow-slate-900/10",
        isAgent &&
          "mr-auto max-w-full border border-slate-200 bg-white text-zinc-950 shadow-slate-200/60 sm:max-w-[44rem]",
        !isUser &&
          !isAgent &&
          "mx-auto max-w-full border border-slate-200 bg-white text-slate-700 shadow-slate-200/60 sm:max-w-[44rem]"
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
        <div className="grid grid-cols-1 gap-2">
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
  const text = event.text ?? "";
  const data = event.dataJson ?? "";
  // Tool events carry a short label in `text` (e.g. the command) and the full
  // structured payload in `dataJson` (e.g. captured output). Show both so the
  // trace is actually inspectable, but avoid duplicating when they're equal.
  if (text && data && text !== data) {
    return `${text}\n${data}`;
  }
  return text || data;
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
  const leftSeq = eventSequenceNumber(left);
  const rightSeq = eventSequenceNumber(right);
  if (leftSeq !== undefined && rightSeq !== undefined) {
    return leftSeq - rightSeq;
  }
  return 0;
}

function compareLiveDeltaEntries(left: LiveDeltaEntry, right: LiveDeltaEntry) {
  if (left.afterSeq !== right.afterSeq) {
    return left.afterSeq - right.afterSeq;
  }
  return left.order - right.order;
}

function rememberPersistedEventSeq(
  event: ConversationEvent,
  latestPersistedSeqRef: { current: number }
) {
  const seq = eventSequenceNumber(event);
  if (seq !== undefined && seq > latestPersistedSeqRef.current) {
    latestPersistedSeqRef.current = seq;
  }
}

function eventSequenceNumber(event: ConversationEvent) {
  const seq = Number(event.seq);
  return Number.isFinite(seq) && seq > 0 ? seq : undefined;
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
