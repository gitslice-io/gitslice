import {
  type MutableRefObject,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef
} from "react";

export interface UseStickToBottomScrollOptions {
  /**
   * Scroll container element ref. When omitted, uses the document scrolling element
   * (`document.scrollingElement ?? document.documentElement`) and listens on `window`.
   */
  containerRef?: RefObject<HTMLElement | null>;
  /**
   * Optional element ref observed via `ResizeObserver` so asynchronously settling content
   * (decoded images, web fonts) re-pins the scroll container when parked at the bottom.
   */
  observeResizeRef?: RefObject<HTMLElement | null>;
  /** Dependencies that trigger bottom-pinning when `stickToBottomRef.current` is true. */
  pinDeps: readonly unknown[];
  /** Dependencies that trigger prepend-anchor restoration when older content is prepended. */
  prependDeps?: readonly unknown[];
  /** Distance from bottom (px) within which the reader is considered parked at the bottom. */
  thresholdPx?: number;
}

export interface UseStickToBottomScrollResult {
  stickToBottomRef: MutableRefObject<boolean>;
  prependAnchorRef: MutableRefObject<number | null>;
  capturePrependAnchor: () => void;
  stickToBottom: () => void;
}

function resolveScroller(containerRef?: RefObject<HTMLElement | null>): HTMLElement | Element | null {
  if (containerRef) {
    return containerRef.current;
  }
  if (typeof document === "undefined") {
    return null;
  }
  return document.scrollingElement ?? document.documentElement;
}

export function useStickToBottomScroll({
  containerRef,
  observeResizeRef,
  pinDeps,
  prependDeps = [],
  thresholdPx = 96
}: UseStickToBottomScrollOptions): UseStickToBottomScrollResult {
  const stickToBottomRef = useRef(true);
  const prependAnchorRef = useRef<number | null>(null);

  const stickToBottom = useCallback(() => {
    stickToBottomRef.current = true;
  }, []);

  const capturePrependAnchor = useCallback(() => {
    const scroller = resolveScroller(containerRef);
    if (scroller) {
      prependAnchorRef.current = scroller.scrollHeight - scroller.scrollTop;
    }
  }, [containerRef]);

  // Pin to bottom on new content before paint when the reader is parked at the bottom.
  useLayoutEffect(() => {
    if (!stickToBottomRef.current) {
      return;
    }
    const scroller = resolveScroller(containerRef);
    if (scroller) {
      scroller.scrollTop = scroller.scrollHeight;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, pinDeps);

  // Restore scroll position when older content is prepended above the viewport.
  useLayoutEffect(() => {
    if (prependAnchorRef.current == null) {
      return;
    }
    const scroller = resolveScroller(containerRef);
    if (scroller) {
      scroller.scrollTop = scroller.scrollHeight - prependAnchorRef.current;
    }
    prependAnchorRef.current = null;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, prependDeps);

  // Track whether the reader is parked near the bottom of the scroll container.
  useEffect(() => {
    function onScroll() {
      const scroller = resolveScroller(containerRef);
      if (!scroller) {
        return;
      }
      stickToBottomRef.current =
        scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < thresholdPx;
    }

    if (containerRef) {
      const node = containerRef.current;
      if (!node) {
        return;
      }
      node.addEventListener("scroll", onScroll, { passive: true });
      return () => node.removeEventListener("scroll", onScroll);
    }

    if (typeof window === "undefined") {
      return;
    }
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
    // Re-bind when container element mounts/changes (e.g. conditional <pre>).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef, thresholdPx, ...(containerRef ? pinDeps : [])]);

  // Re-pin to bottom when observed content resizes while parked at the bottom.
  useEffect(() => {
    const node = observeResizeRef?.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(() => {
      if (!stickToBottomRef.current) {
        return;
      }
      const scroller = resolveScroller(containerRef);
      if (scroller) {
        scroller.scrollTop = scroller.scrollHeight;
      }
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [containerRef, observeResizeRef]);

  return {
    stickToBottomRef,
    prependAnchorRef,
    capturePrependAnchor,
    stickToBottom
  };
}
