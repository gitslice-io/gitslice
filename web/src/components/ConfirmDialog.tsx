import type { ReactNode } from "react";

import { cn } from "../lib/cn";
import { Popup } from "./Popup";

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  description?: string;
  /** Body/explanation text shown in the dialog. */
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Visual tone of the confirm button. */
  tone?: "default" | "danger";
  /** Disables the confirm button + shows busy state. */
  busy?: boolean;
  onConfirm(): void;
  onCancel(): void;
}

export function ConfirmDialog({
  busy = false,
  cancelLabel = "Cancel",
  confirmLabel = "Confirm",
  description,
  message,
  onCancel,
  onConfirm,
  open,
  title,
  tone = "default"
}: ConfirmDialogProps) {
  const handleCancel = () => {
    if (!busy) {
      onCancel();
    }
  };

  return (
    <Popup
      description={description}
      onClose={handleCancel}
      open={open}
      title={title}
    >
      <div className="grid gap-4">
        {message ? (
          <div className="text-sm leading-6 text-slate-600 dark:text-zinc-400">{message}</div>
        ) : null}
        <div className="flex justify-end gap-2">
          <button
            className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-2 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-100 dark:disabled:bg-zinc-800 disabled:text-slate-400 dark:disabled:text-zinc-500"
            disabled={busy}
            onClick={handleCancel}
            type="button"
          >
            {cancelLabel}
          </button>
          <button
            className={cn(
              "rounded-md px-3 py-2 text-sm font-semibold text-white transition active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-zinc-600",
              tone === "danger"
                ? "bg-rose-600 hover:bg-rose-500"
                : "bg-zinc-950 hover:bg-zinc-800"
            )}
            disabled={busy}
            onClick={onConfirm}
            type="button"
          >
            {busy ? `${confirmLabel}...` : confirmLabel}
          </button>
        </div>
      </div>
    </Popup>
  );
}
