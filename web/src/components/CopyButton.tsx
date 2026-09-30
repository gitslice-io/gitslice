import { useEffect, useRef, useState } from "react";

import { cn } from "../lib/cn";

interface CopyButtonProps {
  className?: string;
  /** Icon only: for buttons overlaid on content, where a label would grow over it. */
  compact?: boolean;
  label?: string;
  text: string;
}

async function copyText(text: string) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // A denied Clipboard API can still succeed through the browser fallback.
  }

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("aria-hidden", "true");
  textarea.setAttribute("readonly", "");
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  document.body.append(textarea);
  textarea.select();

  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    textarea.remove();
  }
}

export function CopyButton({
  className,
  compact = false,
  label = "to clipboard",
  text
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const resetTimer = useRef<ReturnType<typeof setTimeout>>();

  useEffect(() => {
    return () => {
      if (resetTimer.current) {
        clearTimeout(resetTimer.current);
      }
    };
  }, []);

  async function handleCopy() {
    if (!(await copyText(text))) {
      return;
    }

    setCopied(true);
    if (resetTimer.current) {
      clearTimeout(resetTimer.current);
    }
    resetTimer.current = setTimeout(() => setCopied(false), 1500);
  }

  return (
    <button
      aria-label={`Copy ${label}`}
      className={cn(
        "inline-flex min-h-8 items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2 py-1 text-xs font-medium text-slate-600 outline-none transition hover:border-slate-300 hover:text-zinc-950 motion-safe:active:scale-[0.98] focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:border-zinc-600 dark:hover:text-zinc-50 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950",
        className
      )}
      onClick={handleCopy}
      type="button"
    >
      {copied ? (
        <svg
          aria-hidden="true"
          className="size-3.5"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="2"
          viewBox="0 0 24 24"
        >
          <path d="m5 12 4 4L19 6" />
        </svg>
      ) : (
        <svg
          aria-hidden="true"
          className="size-3.5"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="1.8"
          viewBox="0 0 24 24"
        >
          <rect height="14" rx="2" width="12" x="8" y="6" />
          <path d="M16 6V4a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h2" />
        </svg>
      )}
      {copied && !compact ? <span aria-hidden="true">Copied</span> : null}
      <span aria-live="polite" className="sr-only" role="status">
        {copied ? "Copied" : ""}
      </span>
    </button>
  );
}
