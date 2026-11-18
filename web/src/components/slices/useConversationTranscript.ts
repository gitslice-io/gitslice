import { type RefObject, useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { ConversationEvent } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { useConnectStream } from "../../lib/useConnectStream";
import { useStickToBottomScroll } from "../../lib/useStickToBottomScroll";
import {
  type LiveDeltaEntry,
  agentTurnLabel,
  appendConversationEvent,
  coalesceConversationEvents,
  groupConversationEvents,
  isLiveDelta,
  isPatchsetStatusEvent,
  oldestSeqOf,
  rememberPersistedEventSeq
} from "./conversationEvents";
import { getErrorMessage } from "./SlicePageParts";

// How many persisted events to tail-load on open, and to page in per "load
// earlier" step.
export const EVENT_PAGE_SIZE = 200;

export interface UseConversationTranscriptOptions {
  api: ApiClient;
  conversationId: string;
  agentOffline: boolean;
  messagesRef: RefObject<HTMLDivElement | null>;
  topSentinelRef: RefObject<HTMLDivElement | null>;
}

export function useConversationTranscript({
  api,
  conversationId,
  agentOffline,
  messagesRef,
  topSentinelRef
}: UseConversationTranscriptOptions) {
  const [events, setEvents] = useState<ConversationEvent[]>([]);
  const [liveDeltas, setLiveDeltas] = useState<Map<string, LiveDeltaEntry>>(
    () => new Map()
  );
  const [historyLoaded, setHistoryLoaded] = useState(false);
  const [reservedMinHeight, setReservedMinHeight] = useState<number | null>(null);
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [isLoadingEarlier, setIsLoadingEarlier] = useState(false);
  const [earlierError, setEarlierError] = useState("");

  const latestPersistedSeqRef = useRef(0);
  const liveDeltaOrderRef = useRef(0);
  const oldestLoadedSeqRef = useRef(0);
  const loadEarlierRef = useRef<() => void>(() => {});

  const {
    prependAnchorRef,
    capturePrependAnchor,
    stickToBottom
  } = useStickToBottomScroll({
    observeResizeRef: messagesRef,
    pinDeps: [events.length, liveDeltas],
    prependDeps: [events.length],
    thresholdPx: 96
  });

  // Reset transcript state only when the conversation actually changes.
  useEffect(() => {
    setReservedMinHeight(messagesRef.current?.offsetHeight ?? null);
    setEvents([]);
    setLiveDeltas(new Map());
    latestPersistedSeqRef.current = 0;
    liveDeltaOrderRef.current = 0;
    setHistoryLoaded(false);
    setHasMoreHistory(false);
    setIsLoadingEarlier(false);
    setEarlierError("");
    oldestLoadedSeqRef.current = 0;
    prependAnchorRef.current = null;
    stickToBottom();
  }, [conversationId, messagesRef, prependAnchorRef, stickToBottom]);

  const backfillHistory = useCallback(
    async (signal: AbortSignal) => {
      if (latestPersistedSeqRef.current === 0) {
        try {
          const history = await api.getConversationEvents({
            conversationId,
            afterSeq: 0,
            limit: EVENT_PAGE_SIZE
          });
          if (signal.aborted) {
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
          setHasMoreHistory(historyEvents.length >= EVENT_PAGE_SIZE);
          setHistoryLoaded(true);
          setReservedMinHeight(null);
        } catch {
          // Fall through to streaming from seq 0.
        }
      }
      if (!signal.aborted) {
        setHistoryLoaded(true);
        setReservedMinHeight(null);
      }
    },
    [api, conversationId]
  );

  const rehydrateResolvedTranscript = useCallback(
    async (signal: AbortSignal) => {
      let refreshed;
      try {
        refreshed = await api.getConversationEvents({
          conversationId,
          afterSeq: Math.max(oldestLoadedSeqRef.current - 1, 0)
        });
      } catch {
        return;
      }
      if (signal.aborted) {
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
    },
    [api, conversationId]
  );

  const { streamError, isStreamReconnecting, reconnectNow } = useConnectStream<ConversationEvent>({
    streamKey: conversationId,
    attemptDeps: [api],
    afterSeqRef: latestPersistedSeqRef,
    onAttemptStart: () => {
      setLiveDeltas(new Map());
    },
    beforeStream: backfillHistory,
    openStream: (afterSeq, signal) =>
      api.streamConversation({ conversationId, afterSeq }, signal),
    getSeq: (event) => event.seq,
    onItem: (event, signal) => {
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
        return;
      }
      setEvents((current) => appendConversationEvent(current, event));
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
      if (isPatchsetStatusEvent(event)) {
        void rehydrateResolvedTranscript(signal);
      }
    },
    reconnectOnCloseMessage: "Stream closed before the conversation finished.",
    reconnectOnError: true,
    formatError: getErrorMessage
  });

  const loadEarlierMessages = useCallback(async () => {
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
      capturePrependAnchor();
      for (const event of older) {
        rememberPersistedEventSeq(event, latestPersistedSeqRef);
      }
      setEvents((current) => coalesceConversationEvents([...older, ...current]));
      const newOldest = oldestSeqOf(older);
      if (newOldest > 0) {
        oldestLoadedSeqRef.current = newOldest;
      }
      setHasMoreHistory(older.length >= EVENT_PAGE_SIZE);
    } catch (error) {
      prependAnchorRef.current = null;
      setEarlierError(getErrorMessage(error));
    } finally {
      setIsLoadingEarlier(false);
    }
  }, [api, capturePrependAnchor, conversationId, hasMoreHistory, isLoadingEarlier, prependAnchorRef]);

  loadEarlierRef.current = loadEarlierMessages;

  // Auto-load the previous page when the top sentinel scrolls into view.
  useEffect(() => {
    const sentinel = topSentinelRef.current;
    if (!sentinel || !hasMoreHistory || typeof IntersectionObserver === "undefined") {
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
  }, [hasMoreHistory, conversationId, topSentinelRef]);

  const appendSentEvent = useCallback((sentEvent: ConversationEvent) => {
    rememberPersistedEventSeq(sentEvent, latestPersistedSeqRef);
    setEvents((current) => appendConversationEvent(current, sentEvent));
  }, []);

  const conversationItems = useMemo(
    () => groupConversationEvents(events, [...liveDeltas.values()]),
    [events, liveDeltas]
  );

  const agentWorkingLabel = useMemo(
    () => (agentOffline ? null : agentTurnLabel(events, liveDeltas)),
    [agentOffline, events, liveDeltas]
  );

  return {
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
  };
}
