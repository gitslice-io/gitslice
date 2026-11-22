import { useId, type ReactNode } from "react";

import { cn } from "../../lib/cn";
import { Button } from "./Button";
import { useNativeDialog } from "./useNativeDialog";

export type DrawerSize = "md" | "xl" | "5xl";

const SIZE_CLASSES: Record<DrawerSize, string> = {
  md: "max-w-md",
  xl: "max-w-xl",
  "5xl": "max-w-5xl"
};

export interface DrawerProps {
  open: boolean;
  onClose(): void;
  title: ReactNode;
  titleClassName?: string;
  subtitle?: ReactNode;
  badge?: ReactNode;
  headerExtra?: ReactNode;
  ariaLabel?: string;
  closeLabel?: string;
  closeVariant?: "icon" | "button";
  closeText?: string;
  size?: DrawerSize;
  children: ReactNode;
}

/**
 * Right-anchored native modal `<dialog>` drawer with `showModal()`,
 * `closedby="any"` light-dismiss + Safari pointer fallback, and a shared header.
 */
export function Drawer({
  open,
  onClose,
  title,
  titleClassName,
  subtitle,
  badge,
  headerExtra,
  ariaLabel,
  closeLabel = "Close",
  closeVariant = "icon",
  closeText = "Close",
  size = "md",
  children
}: DrawerProps) {
  const headingId = useId();
  const { dialogRef, dialogProps, requestClose } = useNativeDialog({ open, onClose });

  if (!open) {
    return null;
  }

  return (
    <dialog
      aria-label={ariaLabel}
      aria-labelledby={ariaLabel ? undefined : headingId}
      className={cn(
        "fixed inset-y-0 right-0 left-auto z-50 m-0 h-dvh max-h-none w-full min-w-0 border-0 bg-white p-0 text-zinc-950 outline-none shadow-xl backdrop:bg-black/35 open:flex open:flex-col dark:bg-zinc-900 dark:text-zinc-50",
        SIZE_CLASSES[size]
      )}
      ref={dialogRef}
      {...dialogProps}
    >
      <div className="shrink-0 border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2
                className={cn(
                  "text-base font-semibold tracking-tight text-zinc-950 dark:text-zinc-50",
                  titleClassName
                )}
                id={headingId}
              >
                {title}
              </h2>
              {badge}
            </div>
            {subtitle}
          </div>
          {closeVariant === "button" ? (
            <Button className="shrink-0 font-semibold" onClick={requestClose} size="sm" variant="secondary">
              {closeText}
            </Button>
          ) : (
            <button
              aria-label={closeLabel}
              className="inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-zinc-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 active:scale-[0.98] dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50 [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:min-w-11"
              onClick={requestClose}
              type="button"
            >
              <svg
                aria-hidden="true"
                className="h-4 w-4"
                fill="none"
                stroke="currentColor"
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth="2"
                viewBox="0 0 24 24"
              >
                <path d="M18 6 6 18" />
                <path d="m6 6 12 12" />
              </svg>
            </button>
          )}
        </div>
        {headerExtra}
      </div>
      {children}
    </dialog>
  );
}
