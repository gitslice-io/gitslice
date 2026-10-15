import {
  FormEvent,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState
} from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { Conversation, ConversationEvent } from "../../api/types";
import { type ApiClient } from "../../api/useApi";
import { ConfirmDialog } from "../ConfirmDialog";
import { cn } from "../../lib/cn";
import { getErrorMessage } from "./SlicePageParts";

import {
  type LiveDeltaEntry,
  groupConversationEvents,
  agentTurnLabel,
  rememberPersistedEventSeq,
  coalesceConversationEvents,
  oldestSeqOf,
  isLiveDelta,
  appendConversationEvent,
  isPatchsetStatusEvent,
  updateConversationList,
  traceGroupKey,
  eventKey
} from "./conversationEvents";
import {
  ConversationStatusDot,
  TranscriptSkeleton,
  ConversationTraceGroup,
  LiveDeltaBubble,
  ConversationEventBubble,
  AgentWorkingIndicator
} from "./ConversationParts";

const STREAM_RECONNECT_DELAY_MS = 1500;
// How many persisted events to tail-load on open, and to page in per "load
// earlier" step. A conversation's transcript can be thousands of events (each
// reasoning/tool trace is its own event); rendering them all in one commit
// stalls the main thread and makes opening a conversation lurch. We instead open
// on the newest EVENT_PAGE_SIZE events and page older ones in as the user
// scrolls up. This counts events, not visible messages — trace events are
// plentiful, so a page is typically a few dozen bubbles.
const EVENT_PAGE_SIZE = 200;

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
  const [isCloseConfirmOpen, setIsCloseConfirmOpen] = useState(false);
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
  // The transcript scroll region, measured on a conversation switch so we can
  // hold its height while the next conversation's history loads (see below).
  const messagesRef = useRef<HTMLDivElement>(null);
  // While switching conversations, pin the transcript region to the outgoing
  // conversation's height so the page (the window is the scroll container)
  // doesn't collapse and yank the scroll upward, only to snap back to the
  // bottom when history arrives. Null means "no reservation"; it is set on
  // switch and cleared in the same commit that the new history lands.
  const [reservedMinHeight, setReservedMinHeight] = useState<number | null>(
    null
  );
  // Reverse-pagination state for the tail-loaded transcript. The initial load
  // (backfillHistory) fetches only the newest EVENT_PAGE_SIZE events; older ones
  // page in from the top as the user scrolls up. `hasMoreHistory` gates the
  // "load earlier" affordance, `isLoadingEarlier` guards against overlapping
  // page fetches, and the seq of the oldest loaded event is the cursor for the
  // next page. The seq is mirrored in a ref so the stream effect's closures and
  // the IntersectionObserver read the latest value without re-subscribing.
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [isLoadingEarlier, setIsLoadingEarlier] = useState(false);
  const [earlierError, setEarlierError] = useState("");
  const oldestLoadedSeqRef = useRef(0);
  // Set just before an older page is prepended, holding the pre-prepend distance
  // from the bottom of the scroll container. The layout effect below restores it
  // so the reader stays parked on the same message instead of being shoved down
  // by the newly inserted history (the window is the scroll container, and
  // Safari has no native scroll anchoring to lean on).
  const prependAnchorRef = useRef<number | null>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  // Holds the latest loadEarlierMessages closure so the IntersectionObserver
  // effect can call it without re-subscribing on every render.
  const loadEarlierRef = useRef<() => void>(() => {});

  const title = useMemo(
    () => conversation?.title || conversationId,
    [conversation?.title, conversationId]
  );
  const conversationItems = useMemo(
    () => groupConversationEvents(events, [...liveDeltas.values()]),
    [events, liveDeltas]
  );
  const agentOffline = conversation?.daemonOnline === false;
  const conversationStatus = conversation?.status || "active";
  const isConversationActive = conversationStatus === "active";
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
    // Capture the outgoing transcript's height before we clear it, so the page
    // holds its scroll position while the next conversation's history loads
    // instead of collapsing (scroll up) then snapping to the bottom (scroll
    // down). Cleared when the new history lands (see backfillHistory / run).
    setReservedMinHeight(messagesRef.current?.offsetHeight ?? null);
    setEvents([]);
    setLiveDeltas(new Map());
    latestPersistedSeqRef.current = 0;
    liveDeltaOrderRef.current = 0;
    setStreamError("");
    setIsStreamReconnecting(false);
    setSendError("");
    setDraft("");
    setHistoryLoaded(false);
    // Reset reverse-pagination: the next conversation tail-loads from scratch.
    setHasMoreHistory(false);
    setIsLoadingEarlier(false);
    setEarlierError("");
    oldestLoadedSeqRef.current = 0;
    prependAnchorRef.current = null;
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
          afterSeq: 0,
          limit: EVENT_PAGE_SIZE
        });
        if (controller.signal.aborted) {
          return;
        }
        const historyEvents = history.events ?? [];
        if (historyEvents.length) {
          for (const event of historyEvents) {
            rememberPersistedEventSeq(event, latestPersistedSeqRef);
          }
          setEvents(coalesceConversationEvents(historyEvents));
          oldestLoadedSeqRef.current = oldestSeqOf(historyEvents);
        }
        // A full page means there may be older events to page in from the top.
        // Compare the raw returned count (not the coalesced length) against the
        // page size so delta/final merges don't falsely report "no more".
        setHasMoreHistory(historyEvents.length >= EVENT_PAGE_SIZE);
        // Land the transcript and release the height reservation together, in
        // one commit: the scroll effect then pins the new history to the bottom
        // in a single pre-paint pass instead of a visible up-then-down jump.
        setHistoryLoaded(true);
        setReservedMinHeight(null);
      } catch {
        // Fall through to streaming from seq 0, which still backfills the
        // transcript (just event-by-event) — better than rendering nothing.
      }
    }

    // When a turn captures a patchset, links in that turn can upgrade from the
    // slice-file view to the precise changeset+patchset URL because the server
    // can now resolve them against the just-created patchset. Re-read and
    // rebuild the transcript from the server-resolved events; this also clears
    // any live token bubble when the runtime never emitted a final message event.
    async function rehydrateResolvedTranscript() {
      let refreshed;
      try {
        // Re-resolve only the currently loaded window (from the oldest loaded
        // event onward), not the whole transcript — otherwise this would
        // re-expand a tail-loaded conversation to its full history. Older events
        // get resolved by the server when they page in.
        refreshed = await api.getConversationEvents({
          conversationId,
          afterSeq: Math.max(oldestLoadedSeqRef.current - 1, 0)
        });
      } catch {
        return;
      }
      if (controller.signal.aborted) {
        return;
      }
      const refreshedEvents = refreshed.events ?? [];
      if (!refreshedEvents.length) {
        return;
      }
      for (const event of refreshedEvents) {
        rememberPersistedEventSeq(event, latestPersistedSeqRef);
      }
      setEvents(coalesceConversationEvents(refreshedEvents));
      setLiveDeltas(new Map());
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
          // A patchset was just captured for this turn: re-resolve the turn's
          // file links in place so they upgrade from slice-file to changeset URLs.
          if (isPatchsetStatusEvent(event)) {
            void rehydrateResolvedTranscript();
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
      // Safety net for the reconnect / empty-history / fetch-error paths that
      // backfillHistory returns from without flipping these. When backfill did
      // set them, these are no-ops and React bails out (no extra commit).
      setHistoryLoaded(true);
      setReservedMinHeight(null);
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

  // Hold the reader's scroll position when an older page is prepended. Runs as a
  // layout effect (before paint) so the corrected scrollTop lands in the same
  // frame the history renders — no visible jump. The stick-to-bottom effect
  // above early-returns for this commit because loading earlier only happens
  // while scrolled up (not stuck), so the two never fight over scrollTop.
  useLayoutEffect(() => {
    if (prependAnchorRef.current == null) {
      return;
    }
    const scroller = document.scrollingElement ?? document.documentElement;
    // Restore the pre-prepend distance from the bottom: the inserted history
    // pushed existing content down by exactly the scrollHeight delta, so this
    // keeps the same message under the viewport.
    scroller.scrollTop = scroller.scrollHeight - prependAnchorRef.current;
    prependAnchorRef.current = null;
  }, [events.length]);

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

  // Keep the latest message in view while content above the viewport settles
  // *after* the scroll was pinned — images decoding to their real height, web
  // fonts swapping in — which grows the page without a React re-render, so the
  // useLayoutEffect pin above never fires for it. A ResizeObserver on the
  // transcript re-pins to the bottom whenever it grows and the reader is still
  // parked there. When they've scrolled up to read it stays put (guarded by
  // stickToBottomRef), so streamed tokens or a settling image don't yank them.
  // (Loading earlier pages is likewise unaffected: that only happens while
  // scrolled up, and its own layout effect holds the reader's position.)
  useEffect(() => {
    const node = messagesRef.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(() => {
      if (!stickToBottomRef.current) {
        return;
      }
      const scroller = document.scrollingElement ?? document.documentElement;
      scroller.scrollTop = scroller.scrollHeight;
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  // Auto-load the previous page when the top sentinel scrolls into view, so the
  // transcript fills upward as the reader scrolls without a click. The observer
  // calls the latest loadEarlierMessages via a ref, so it doesn't re-subscribe
  // on every render; it re-subscribes only when there is (or stops being) more
  // history, or on a conversation switch. The rootMargin pre-loads a little
  // before the sentinel reaches the viewport so history is usually already there
  // by the time the user gets to the top.
  useEffect(() => {
    const sentinel = topSentinelRef.current;
    if (
      !sentinel ||
      !hasMoreHistory ||
      typeof IntersectionObserver === "undefined"
    ) {
      // No observer (SSR / jsdom / unsupported): the visible "Load earlier
      // messages" button is the fallback way to page history in.
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          loadEarlierRef.current();
        }
      },
      { rootMargin: "400px 0px 0px 0px" }
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [hasMoreHistory, conversationId]);

  useEffect(
    () => () => {
      if (copyTimerRef.current !== undefined) {
        clearTimeout(copyTimerRef.current);
      }
    },
    []
  );

  const closeConversationMutation = useMutation({
    mutationFn: () => api.closeConversation({ conversationId }),
    onSuccess: (updatedConversation) => {
      queryClient.setQueryData<Conversation>(
        ["conversation", conversationId],
        updatedConversation
      );
      queryClient.setQueriesData<Conversation[]>(
        { queryKey: ["sliceConversations"] },
        (current) =>
          updateConversationList(current, conversationId, updatedConversation)
      );
      queryClient.setQueryData<Conversation[]>(
        ["recentConversations"],
        (current) =>
          updateConversationList(current, conversationId, updatedConversation)
      );
      void queryClient.invalidateQueries({
        queryKey: ["conversation", conversationId]
      });
      void queryClient.invalidateQueries({ queryKey: ["sliceConversations"] });
      void queryClient.invalidateQueries({ queryKey: ["recentConversations"] });
      setIsCloseConfirmOpen(false);
    }
  });

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

  // Page in the events immediately preceding the oldest one currently loaded.
  // Guarded so overlapping fetches (observer + button, or rapid scrolls) don't
  // stack, and so it no-ops once the transcript's start is reached.
  async function loadEarlierMessages() {
    const oldest = oldestLoadedSeqRef.current;
    if (isLoadingEarlier || !hasMoreHistory || oldest <= 1) {
      return;
    }
    setIsLoadingEarlier(true);
    setEarlierError("");
    try {
      const page = await api.getConversationEvents({
        conversationId,
        beforeSeq: oldest - 1,
        limit: EVENT_PAGE_SIZE
      });
      const older = page.events ?? [];
      if (!older.length) {
        setHasMoreHistory(false);
        return;
      }
      // Capture the scroll geometry before the prepend so the layout effect can
      // hold the reader's position once the older events render.
      const scroller = document.scrollingElement ?? document.documentElement;
      prependAnchorRef.current = scroller.scrollHeight - scroller.scrollTop;
      for (const event of older) {
        rememberPersistedEventSeq(event, latestPersistedSeqRef);
      }
      setEvents((current) =>
        coalesceConversationEvents([...older, ...current])
      );
      const newOldest = oldestSeqOf(older);
      if (newOldest > 0) {
        oldestLoadedSeqRef.current = newOldest;
      }
      // A short page means we've reached the start of the transcript.
      setHasMoreHistory(older.length >= EVENT_PAGE_SIZE);
    } catch (error) {
      // Drop the anchor so a failed fetch doesn't wrongly shift a later commit.
      prependAnchorRef.current = null;
      setEarlierError(getErrorMessage(error));
    } finally {
      setIsLoadingEarlier(false);
    }
  }

  // Keep the ref pointed at the latest closure so the IntersectionObserver reads
  // current state (oldest seq, loading flag) without re-subscribing per render.
  loadEarlierRef.current = loadEarlierMessages;

  return (
    <div className="flex min-h-[calc(100dvh-12rem)] flex-col">
      <div className="flex flex-1 flex-col bg-slate-50/60 dark:bg-zinc-950/60">
        {/* The header lives at the top of the scroll region rather than pinned
            above it, so it collapses out of view as you scroll the transcript —
            like the changeset detail page, whose header is normal page flow.
            Since the view is anchored to the latest message, the header starts
            scrolled away, handing its space to the conversation. The page
            breadcrumb lives outside this panel, pinned above it like every
            other slice page. */}
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
                    : "text-slate-400 dark:text-zinc-500 hover:text-zinc-950 dark:hover:text-zinc-50",
                )}
                onClick={() => {
                  void copyConversationLink();
                }}
                title={linkCopied ? "Link copied" : "Copy link"}
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
              {!readOnly && isConversationActive ? (
                <button
                  className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-1 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:border-slate-300 dark:hover:border-zinc-700 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-400 dark:disabled:text-zinc-500"
                  disabled={closeConversationMutation.isPending}
                  onClick={() => setIsCloseConfirmOpen(true)}
                  type="button"
                >
                  {closeConversationMutation.isPending ? "Closing..." : "Close"}
                </button>
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

        {/* flex-1 so the transcript fills the space below the header, and the
            content below is bottom-anchored (mt-auto). A short conversation then
            sits at the bottom — where the loading skeleton also anchors — so the
            skeleton->messages swap on a conversation switch happens in place
            instead of jumping the content up from the bottom to the top. */}
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
            // mt-auto bottom-anchors the transcript: it pushes short content to
            // the bottom of the flex column, and collapses to a no-op once the
            // content is tall enough to fill/overflow (long conversations flow
            // from the top and scroll normally, with the top still reachable —
            // unlike justify-end, which would strand the overflowing top).
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
