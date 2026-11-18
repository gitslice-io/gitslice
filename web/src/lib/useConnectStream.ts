import { type MutableRefObject, useCallback, useEffect, useRef, useState } from "react";

export const DEFAULT_STREAM_RECONNECT_DELAY_MS = 1500;

export function parseStreamSeq(raw: number | string | undefined): number | undefined {
  if (raw === undefined || raw === "") {
    return undefined;
  }
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

export function advanceStreamSeq(
  raw: number | string | undefined,
  cursorRef: { current: number }
): void {
  const seq = parseStreamSeq(raw);
  if (seq !== undefined && seq > cursorRef.current) {
    cursorRef.current = seq;
  }
}

function defaultFormatError(error: unknown): string {
  if (error instanceof Error && error.message.trim()) {
    return error.message;
  }
  return "Stream failed.";
}

export interface UseConnectStreamOptions<TItem> {
  /** Stream identity key (e.g., conversationId or runId). Resets cursor & state on change. */
  streamKey: string;
  /** Dependency token that triggers a fresh stream attachment without resetting `afterSeq`. */
  attemptDeps?: readonly unknown[];
  /** Whether the stream should run (defaults to true when streamKey is non-empty). */
  enabled?: boolean;
  /** Optional external cursor ref when the caller also advances `afterSeq` from non-stream RPCs. */
  afterSeqRef?: MutableRefObject<number>;
  /** Async hook run before opening the stream on each attempt (e.g. initial history backfill). */
  beforeStream?: (signal: AbortSignal) => Promise<void>;
  /** Called synchronously when a stream attempt starts (e.g. to clear transient delta state). */
  onAttemptStart?: () => void;
  /** Opens the server-streaming RPC starting after `afterSeq`. */
  openStream: (afterSeq: number, signal: AbortSignal) => AsyncIterable<TItem>;
  /** Called for each received item. */
  onItem: (item: TItem, signal: AbortSignal) => void;
  /** Extracts the sequence number from a streamed item to advance `afterSeqRef`. */
  getSeq?: (item: TItem) => number | string | undefined;
  /**
   * When provided and returning a non-empty message, normal stream completion schedules
   * an automatic reconnect with that message instead of marking `streamClosed = true`.
   */
  reconnectOnCloseMessage?: string | (() => string | null | undefined);
  /** Whether to schedule an automatic reconnect when the stream throws (default: true). */
  reconnectOnError?: boolean | (() => boolean);
  /** Delay before automatic reconnect in ms (default: 1500). */
  reconnectDelayMs?: number;
  /** Formats thrown errors into user-facing strings. */
  formatError?: (error: unknown) => string;
}

export interface UseConnectStreamResult {
  streamError: string;
  isStreamReconnecting: boolean;
  streamClosed: boolean;
  afterSeqRef: MutableRefObject<number>;
  reconnectNow: () => void;
}

export function useConnectStream<TItem>({
  streamKey,
  attemptDeps = [],
  enabled = true,
  afterSeqRef: externalAfterSeqRef,
  beforeStream,
  onAttemptStart,
  openStream,
  onItem,
  getSeq,
  reconnectOnCloseMessage,
  reconnectOnError = true,
  reconnectDelayMs = DEFAULT_STREAM_RECONNECT_DELAY_MS,
  formatError = defaultFormatError
}: UseConnectStreamOptions<TItem>): UseConnectStreamResult {
  const internalAfterSeqRef = useRef(0);
  const afterSeqRef = externalAfterSeqRef ?? internalAfterSeqRef;

  const [streamError, setStreamError] = useState("");
  const [isStreamReconnecting, setIsStreamReconnecting] = useState(false);
  const [streamClosed, setStreamClosed] = useState(false);
  const [retryKey, setRetryKey] = useState(0);

  const callbacksRef = useRef({
    beforeStream,
    onAttemptStart,
    openStream,
    onItem,
    getSeq,
    reconnectOnCloseMessage,
    reconnectOnError,
    formatError
  });
  callbacksRef.current = {
    beforeStream,
    onAttemptStart,
    openStream,
    onItem,
    getSeq,
    reconnectOnCloseMessage,
    reconnectOnError,
    formatError
  };

  useEffect(() => {
    afterSeqRef.current = 0;
    setStreamError("");
    setIsStreamReconnecting(false);
    setStreamClosed(false);
  }, [streamKey, afterSeqRef]);

  useEffect(() => {
    if (!enabled || !streamKey) {
      return;
    }

    const controller = new AbortController();
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

    callbacksRef.current.onAttemptStart?.();
    setStreamError("");
    setIsStreamReconnecting(false);
    setStreamClosed(false);

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
      }, reconnectDelayMs);
    }

    async function run() {
      if (callbacksRef.current.beforeStream) {
        await callbacksRef.current.beforeStream(controller.signal);
        if (controller.signal.aborted) {
          return;
        }
      }

      try {
        const iterable = callbacksRef.current.openStream(
          afterSeqRef.current,
          controller.signal
        );
        for await (const item of iterable) {
          if (controller.signal.aborted) {
            return;
          }
          callbacksRef.current.onItem(item, controller.signal);
          if (callbacksRef.current.getSeq) {
            advanceStreamSeq(callbacksRef.current.getSeq(item), afterSeqRef);
          }
        }
        if (controller.signal.aborted) {
          return;
        }
        const closeMsg =
          typeof callbacksRef.current.reconnectOnCloseMessage === "function"
            ? callbacksRef.current.reconnectOnCloseMessage()
            : callbacksRef.current.reconnectOnCloseMessage;
        if (closeMsg) {
          scheduleReconnect(closeMsg);
        } else {
          setStreamClosed(true);
        }
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        const message = callbacksRef.current.formatError(error);
        const shouldRetry =
          typeof callbacksRef.current.reconnectOnError === "function"
            ? callbacksRef.current.reconnectOnError()
            : callbacksRef.current.reconnectOnError;
        if (shouldRetry) {
          scheduleReconnect(message);
        } else {
          setStreamError(message);
          setStreamClosed(true);
        }
      }
    }

    void run();

    return () => {
      controller.abort();
      if (reconnectTimer !== undefined) {
        clearTimeout(reconnectTimer);
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [streamKey, enabled, retryKey, reconnectDelayMs, ...attemptDeps]);

  const reconnectNow = useCallback(() => {
    setStreamError("");
    setIsStreamReconnecting(false);
    setRetryKey((value) => value + 1);
  }, []);

  return {
    streamError,
    isStreamReconnecting,
    streamClosed,
    afterSeqRef,
    reconnectNow
  };
}
