import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { cn } from "../lib/cn";

interface PopupProps {
  open: boolean;
  onClose(): void;
  title: string;
  description?: string;
  children: ReactNode;
}

// Centered modal dialog. Mirrors the conventions used by the conversation
// drawer (portal to body, fading scrim, Esc-to-close, body-scroll lock, and
// focusing the close button on open) but lays the panel out in the center
// instead of sliding it in from the edge.
export function Popup({
  children,
  description,
  onClose,
  open,
  title
}: PopupProps) {
  const [entered, setEntered] = useState(false);
  const closeButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };

    document.addEventListener("keydown", handleKeyDown);

    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  useEffect(() => {
    if (!open || typeof window === "undefined") {
      setEntered(false);
      return;
    }

    setEntered(false);
    const frame = window.requestAnimationFrame(() => setEntered(true));
    return () => window.cancelAnimationFrame(frame);
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    closeButtonRef.current?.focus();
  }, [open]);

  if (!open || typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button
        aria-label={`Close ${title}`}
        className={cn(
          "fixed inset-0 bg-black/30 transition-opacity duration-200",
          entered ? "opacity-100" : "opacity-0"
        )}
        onClick={onClose}
        type="button"
      />
      <div
        aria-label={title}
        aria-modal="true"
        className={cn(
          "relative z-10 flex max-h-[calc(100dvh-2rem)] w-full max-w-md transform flex-col overflow-hidden rounded-lg bg-white shadow-xl transition duration-200",
          entered ? "scale-100 opacity-100" : "scale-95 opacity-0"
        )}
        role="dialog"
      >
        <div className="flex items-start justify-between gap-3 border-b border-slate-200 px-4 py-3">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-zinc-950">{title}</h2>
            {description ? (
              <p className="mt-1 truncate text-xs text-slate-500">
                {description}
              </p>
            ) : null}
          </div>
          <button
            aria-label={`Close ${title}`}
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-slate-200 bg-white text-sm font-semibold text-slate-600 transition hover:bg-slate-50 hover:text-zinc-950 active:scale-[0.98]"
            onClick={onClose}
            ref={closeButtonRef}
            type="button"
          >
            ✕
          </button>
        </div>
        <div className="min-h-0 overflow-y-auto px-4 py-4">{children}</div>
      </div>
    </div>,
    document.body
  );
}
