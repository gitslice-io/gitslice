import { useCallback, useEffect, useRef, useState } from "react";

export type CopyStatus = "idle" | "copied" | "failed";

export interface UseCopyToClipboardOptions {
  resetMs?: number;
  copiedMessage?: string;
  failedMessage?: string;
}

/**
 * Copies text with `navigator.clipboard.writeText`, manages timer reset
 * across repeat clicks and unmounts, tracks failure instead of claiming success,
 * and deduplicates repeated screen-reader announcements.
 */
export function useCopyToClipboard({
  resetMs = 1800,
  copiedMessage = "Copied",
  failedMessage = "Copy failed"
}: UseCopyToClipboardOptions = {}) {
  const [status, setStatus] = useState<CopyStatus>("idle");
  const [announcement, setAnnouncement] = useState("");
  const timerRef = useRef<number | undefined>(undefined);
  const toggleRef = useRef(false);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== undefined) {
      window.clearTimeout(timerRef.current);
      timerRef.current = undefined;
    }
  }, []);

  useEffect(() => clearTimer, [clearTimer]);

  const reset = useCallback(() => {
    clearTimer();
    setStatus("idle");
    setAnnouncement("");
  }, [clearTimer]);

  const scheduleReset = useCallback(() => {
    clearTimer();
    if (resetMs > 0) {
      timerRef.current = window.setTimeout(() => {
        setStatus("idle");
        setAnnouncement("");
        timerRef.current = undefined;
      }, resetMs);
    }
  }, [clearTimer, resetMs]);

  const announce = useCallback((base: string) => {
    toggleRef.current = !toggleRef.current;
    setAnnouncement(toggleRef.current ? base : `${base}\u00A0`);
  }, []);

  const copy = useCallback(
    async (text: string): Promise<boolean> => {
      if (
        typeof navigator === "undefined" ||
        !navigator.clipboard ||
        typeof navigator.clipboard.writeText !== "function"
      ) {
        setStatus("failed");
        announce(failedMessage);
        scheduleReset();
        return false;
      }

      try {
        await navigator.clipboard.writeText(text);
        setStatus("copied");
        announce(copiedMessage);
        scheduleReset();
        return true;
      } catch {
        setStatus("failed");
        announce(failedMessage);
        scheduleReset();
        return false;
      }
    },
    [announce, copiedMessage, failedMessage, scheduleReset]
  );

  return {
    announcement,
    copied: status === "copied",
    copy,
    failed: status === "failed",
    reset,
    status
  };
}
