import {
  FormEvent,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState
} from "react";

import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";

import type { Conversation, ConversationEvent } from "../../api/types";
import { useApi, type ApiClient } from "../../api/useApi";
import { cn } from "../../lib/cn";
import { renderMarkdownToHtml } from "../../lib/markdown";
import { getErrorMessage } from "./SlicePageParts";

const STREAM_RECONNECT_DELAY_MS = 1500;
// Control event the daemon emits when an agent turn ends. It is not part of the
// visible transcript; it only closes the "agent is working" indicator. A turn
// can emit several finalized messages before it finishes, so this marker — not a
// finalized message — is the reliable end-of-turn signal.
const TURN_COMPLETE_TYPE = "turn_complete";

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
  const [linkCopied, setLinkCopied] = useState(false);
  // False until the persisted transcript has been backfilled in one batch (see
  // the stream effect). Gates the empty state so "No messages yet" doesn't flash
  // before history arrives, and lets the view render the whole transcript in a
  // single commit instead of one snap-to-bottom per replayed event.
  const [historyLoaded, setHistoryLoaded] = useState(false);
  // Bumping retryKey re-runs the stream effect after a stream stop, without
  // needing to remount the component or change conversationId.
  const [retryKey, setRetryKey] = useState(0);
  const latestPersistedSeqRef = useRef(0);
  const liveDeltaOrderRef = useRef(0);
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined
  );
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
  const agentOffline = conversation?.daemonOnline === false;
  // While the agent is mid-turn it can be seconds before any message text
  // arrives (process spin-up, thread resume, reasoning). Surface a "working"
  // pill so the wait reads as activity, not a hang.
  const agentWorkingLabel = useMemo(
    () => (agentOffline ? null : agentTurnLabel(events, liveDeltas)),
    [agentOffline, events, liveDeltas]
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
    setHistoryLoaded(false);
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

    // Backfill the persisted transcript in one request before opening the
    // stream, so the whole history lands in a single commit. Previously the
    // stream was opened at afterSeq 0 and replayed every persisted event one at
    // a time; each arrival grew the page and snapped it to the bottom, so
    // opening a conversation read as a stutter of downward jumps. On a reconnect
    // (retryKey bump without a conversation change) we already hold the history,
    // so latestPersistedSeqRef is non-zero and we skip straight to tailing.
    async function backfillHistory() {
      if (latestPersistedSeqRef.current !== 0) {
        return;
      }
      try {
        const history = await api.getConversationEvents({
          conversationId,
          afterSeq: 0
        });
        if (controller.signal.aborted) {
          return;
        }
        const historyEvents = history.events ?? [];
        if (historyEvents.length) {
          for (const event of historyEvents) {
            rememberPersistedEventSeq(event, latestPersistedSeqRef);
          }
          setEvents((current) =>
            historyEvents.reduce(appendConversationEvent, current)
          );
        }
      } catch {
        // Fall through to streaming from seq 0, which still backfills the
        // transcript (just event-by-event) — better than rendering nothing.
      }
    }

    async function readStream() {
      try {
        for await (const event of api.streamConversation(
          { conversationId, afterSeq: latestPersistedSeqRef.current },
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

    async function run() {
      await backfillHistory();
      if (controller.signal.aborted) {
        return;
      }
      setHistoryLoaded(true);
      await readStream();
    }

    void run();

    return () => {
      controller.abort();
      if (reconnectTimer !== undefined) {
        clearTimeout(reconnectTimer);
      }
    };
  }, [api, conversationId, retryKey]);

  // The whole page is the scroll container now (the composer pins to the
  // viewport bottom via position: sticky, matching the changesets list's
  // scroll model). Auto-scroll on new events only when the user is already
  // parked at the bottom; otherwise scrolling up to read history would be
  // yanked away on every streamed token.
  //
  // useLayoutEffect (not useEffect) so the scroll correction runs after the DOM
  // grows but before the browser paints. With a plain effect the browser first
  // paints the taller transcript at the old scroll position and only then jumps
  // to the bottom, so every appended event flashed a visible downward lurch.
  useLayoutEffect(() => {
    if (!stickToBottomRef.current) {
      return;
    }
    const scroller = document.scrollingElement ?? document.documentElement;
    scroller.scrollTop = scroller.scrollHeight;
    // `liveDeltas` changes identity on every streamed token, so this also keeps
    // the view pinned to the bottom as a message streams in.
  }, [events.length, liveDeltas]);

  // Track whether the user is parked at the bottom of the page so the effect
  // above only follows the stream when they haven't scrolled up to read.
  useEffect(() => {
    function onScroll() {
      const scroller = document.scrollingElement ?? document.documentElement;
      // 96px slop so minor scrollback / scrollbar rounding doesn't detach.
      stickToBottomRef.current =
        scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 96;
    }
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(
    () => () => {
      if (copyTimerRef.current !== undefined) {
        clearTimeout(copyTimerRef.current);
      }
    },
    []
  );

  async function copyConversationLink() {
    if (
      typeof window === "undefined" ||
      typeof navigator === "undefined" ||
      !navigator.clipboard
    ) {
      return;
    }

    try {
      await navigator.clipboard.writeText(window.location.href);
    } catch {
      return;
    }
    setLinkCopied(true);
    if (copyTimerRef.current !== undefined) {
      clearTimeout(copyTimerRef.current);
    }
    copyTimerRef.current = setTimeout(() => {
      setLinkCopied(false);
      copyTimerRef.current = undefined;
    }, 1500);
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
    <div className="flex min-h-[calc(100dvh-12rem)] flex-col">
      <div className="flex-1 bg-slate-50/60">
        {/* The header lives at the top of the scroll region rather than pinned
            above it, so it collapses out of view as you scroll the transcript —
            like the changeset detail page, whose header is normal page flow.
            Since the view is anchored to the latest message, the header starts
            scrolled away, handing its space to the conversation. The page
            breadcrumb lives outside this panel, pinned above it like every
            other slice page. */}
        <div className="border-b border-slate-200 bg-white px-3 py-3 sm:px-5">
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
              <button
                className="rounded-md border border-slate-200 bg-white px-2.5 py-1 text-xs font-semibold text-slate-700 transition hover:border-slate-300 hover:bg-slate-50 hover:text-zinc-950 active:scale-[0.98]"
                onClick={() => {
                  void copyConversationLink();
                }}
                type="button"
              >
                {linkCopied ? "Copied!" : "Copy link"}
              </button>
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
            !historyLoaded ? (
              <div
                aria-hidden
                className="flex min-h-64 items-center justify-center"
              >
                <span className="h-2 w-24 animate-pulse rounded bg-slate-200" />
              </div>
            ) : (
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
            )
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
              {agentWorkingLabel ? (
                <AgentWorkingIndicator label={agentWorkingLabel} />
              ) : null}
            </div>
          )}
        </div>
      </div>

      {readOnly ? null : (
        <form
          className="sticky bottom-0 z-20 border-t border-slate-200 bg-white px-3 py-3 sm:px-5"
          onSubmit={sendMessage}
        >
          {agentOffline ? (
            <div className="mb-3 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-800">
              This agent is offline. Run{" "}
              <code className="rounded bg-white px-1.5 py-0.5 font-mono text-xs text-amber-900">
                gs agent start
              </code>{" "}
              on the machine hosting it to resume the conversation.
            </div>
          ) : null}
          <div className="flex items-end gap-2">
            <textarea
              aria-label="Message"
              className="max-h-48 min-h-16 w-full min-w-0 resize-y rounded-md border border-slate-300 bg-white px-3 py-2 text-sm leading-6 text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
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
              className="shrink-0 rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300"
              disabled={isSending || !draft.trim() || agentOffline}
              type="submit"
            >
              {isSending ? "Sending..." : "Send"}
            </button>
          </div>
          {sendError ? (
            <p className="mt-2 text-sm text-rose-700">{sendError}</p>
          ) : null}
        </form>
      )}
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
    // turn_complete is a control marker (it closes the working indicator), not
    // visible transcript content.
    if ((event.type ?? "").toLowerCase() === TURN_COMPLETE_TYPE) {
      continue;
    }
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

// MessageMarkdown renders an agent message as sanitized markdown. Parsing is
// synchronous so a streaming message re-renders cleanly on every token. The
// prose classes are tuned for chat bubbles: tight vertical rhythm and no
// leading/trailing margin so the rendered block sits flush in the bubble.
function MessageMarkdown({ source }: { source: string }) {
  const html = useMemo(() => renderMarkdownToHtml(source), [source]);
  return (
    <div
      className="prose prose-sm prose-slate max-w-none break-words prose-p:my-2 prose-pre:my-2 prose-pre:border prose-pre:border-slate-200 prose-pre:bg-slate-50 prose-pre:text-zinc-900 prose-code:before:content-none prose-code:after:content-none prose-headings:mb-2 prose-headings:mt-3 first:prose-headings:mt-0 prose-ul:my-2 prose-ol:my-2 prose-li:my-0.5 prose-a:text-sky-700 prose-a:underline [&>:first-child]:mt-0 [&>:last-child]:mb-0"
      dangerouslySetInnerHTML={{ __html: html }}
    />
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
      <MessageMarkdown source={text} />
      <span className="mt-0.5 inline-block h-3.5 w-1.5 animate-pulse bg-zinc-400 align-baseline" />
    </article>
  );
}

// agentTurnLabel reports what the agent appears to be doing while the user is
// waiting on a reply, or null when the agent is idle. The turn is "in flight"
// from the user's most recent message until a finalized agent message, an
// error, or a status event (cancellation / patchset capture) closes it. An
// actively streaming assistant message is its own indicator — the
// LiveDeltaBubble shows an animated cursor — so we return null in that case to
// avoid a redundant second affordance.
function agentTurnLabel(
  events: ConversationEvent[],
  liveDeltas: Map<string, LiveDeltaEntry>
): string | null {
  let lastUserSeq = 0;
  for (const event of events) {
    if ((event.role ?? "").toLowerCase() === "user") {
      const seq = eventSequenceNumber(event);
      if (seq !== undefined && seq > lastUserSeq) {
        lastUserSeq = seq;
      }
    }
  }
  if (lastUserSeq === 0 || liveDeltas.size > 0) {
    return null;
  }

  let label = "Agent is working…";
  for (const event of events) {
    const seq = eventSequenceNumber(event);
    if (seq === undefined || seq <= lastUserSeq) {
      continue;
    }
    const role = (event.role ?? "").toLowerCase();
    const type = (event.type ?? "").toLowerCase();
    // The explicit turn_complete marker, an error, or a status event (e.g.
    // "canceled" or a captured patchset) marks the end of the turn. A finalized
    // agent message does NOT: a turn can emit several messages and keep working
    // (more reasoning / tool calls) before it finishes, so the indicator must
    // stay up until turn_complete.
    if (
      type === TURN_COMPLETE_TYPE ||
      type === "error" ||
      type === "status"
    ) {
      return null;
    }
    // Otherwise the most recent activity hints at what it's doing now.
    if (type === "reasoning" || type === "reasoning_delta") {
      label = "Agent is thinking…";
    } else if (
      role === "tool" ||
      type === "tool_call" ||
      type === "tool_output"
    ) {
      label = "Agent is working…";
    }
  }

  return label;
}

// AgentWorkingIndicator is the animated "agent is busy" affordance shown at the
// tail of the transcript while a turn is in flight but no token stream is yet
// visible.
function AgentWorkingIndicator({ label }: { label: string }) {
  return (
    <div
      aria-label="Agent activity"
      aria-live="polite"
      className="mr-auto flex w-fit items-center gap-2 rounded-md border border-slate-200 bg-white px-3 py-2 text-sm text-slate-600 shadow-sm shadow-slate-200/60"
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
      </div>
      {capturedPatchset ? (
        <div className="grid grid-cols-1 gap-2">
          <p className="font-medium text-zinc-950">
            Captured patchset {capturedPatchset.patchsetNumber}
          </p>
          <CapturedChangesetLink changesetId={capturedPatchset.changesetId} />
        </div>
      ) : event.dataJson && !event.text ? (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-slate-100 p-2 font-mono text-xs text-slate-700">
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
      className="inline-flex w-fit max-w-full items-center rounded-md border border-slate-300 bg-white px-2.5 py-1.5 font-mono text-xs font-semibold text-zinc-900 underline decoration-slate-300 underline-offset-4 transition hover:border-slate-400 hover:decoration-slate-700 active:scale-[0.98]"
      onFocus={prefetchChangeset}
      onMouseEnter={prefetchChangeset}
      params={{ id: changesetId }}
      to="/cs/$id"
    >
      <span className="truncate">changeset {changesetId}</span>
    </Link>
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

  // Each persisted reasoning_delta carries the full cumulative snapshot, so
  // keeping every one stacks the trace with growing-prefix duplicates. Coalesce
  // by item: a new snapshot — or the finalized reasoning event — supersedes the
  // earlier snapshots for the same item. (message_delta is coalesced live, so
  // it never reaches this list.)
  const base =
    event.itemId && isReasoningEvent(event)
      ? current.filter(
          (item) => !(item.itemId === event.itemId && isReasoningDelta(item))
        )
      : current;

  const next = [...base, event];
  if (hasSequence(event)) {
    next.sort(compareConversationEvents);
  }
  return next;
}

function isReasoningDelta(event: ConversationEvent) {
  return (event.type ?? "").toLowerCase() === "reasoning_delta";
}

function isReasoningEvent(event: ConversationEvent) {
  const type = (event.type ?? "").toLowerCase();
  return type === "reasoning_delta" || type === "reasoning";
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
