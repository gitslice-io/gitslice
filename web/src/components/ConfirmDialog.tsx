import type { ReactNode } from "react";

import { Popup } from "./Popup";
import { Button } from "./ui/Button";

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
          <Button
            disabled={busy}
            onClick={handleCancel}
            size="md"
            variant="secondary"
          >
            {cancelLabel}
          </Button>
          <Button
            className={
              tone === "danger"
                ? "border-transparent bg-rose-600 font-semibold text-white hover:bg-rose-500 dark:bg-rose-600 dark:text-white dark:hover:bg-rose-500"
                : "font-semibold"
            }
            disabled={busy}
            onClick={onConfirm}
            size="md"
            variant={tone === "danger" ? "danger" : "primary"}
          >
            {busy ? `${confirmLabel}...` : confirmLabel}
          </Button>
        </div>
      </div>
    </Popup>
  );
}
