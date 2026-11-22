import { useId, type ReactNode } from "react";

import { cn } from "../../lib/cn";
import { Button } from "./Button";
import { useNativeDialog } from "./useNativeDialog";

export interface ModalDialogProps {
  open: boolean;
  onClose(): void;
  title: ReactNode;
  subtitle?: ReactNode;
  ariaLabel?: string;
  closeLabel?: string;
  maxWidthClassName?: string;
  children: ReactNode;
}

/**
 * Centered native modal `<dialog>` shell with `showModal()`, `closedby="any"`
 * light-dismiss + Safari pointer fallback, and a consistent accessible header.
 */
export function ModalDialog({
  open,
  onClose,
  title,
  subtitle,
  ariaLabel,
  closeLabel = "Cancel",
  maxWidthClassName = "max-w-lg",
  children
}: ModalDialogProps) {
  const titleId = useId();
  const { dialogRef, dialogProps, requestClose } = useNativeDialog({ open, onClose });

  if (!open) {
    return null;
  }

  return (
    <dialog
      aria-label={ariaLabel}
      aria-labelledby={ariaLabel ? undefined : titleId}
      className={cn(
        "fixed inset-0 z-[60] m-auto max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] rounded-lg border border-slate-200 bg-white p-0 text-zinc-950 shadow-2xl backdrop:bg-black/45 open:flex open:flex-col dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-50",
        maxWidthClassName
      )}
      ref={dialogRef}
      {...dialogProps}
    >
      <div className="shrink-0 border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-base font-semibold tracking-tight text-zinc-950 dark:text-zinc-50" id={titleId}>
              {title}
            </h2>
            {subtitle}
          </div>
          <Button className="shrink-0" onClick={requestClose} size="xs" variant="secondary">
            {closeLabel}
          </Button>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
    </dialog>
  );
}
