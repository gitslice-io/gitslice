import type { ReactNode } from "react";

import { cn } from "../../lib/cn";
import { useCopyToClipboard, type CopyStatus } from "../../lib/useCopyToClipboard";
import { buttonClassName, type ButtonSize, type ButtonVariant } from "./Button";

export interface CopyButtonProps {
  value: string | (() => string);
  label?: ReactNode;
  copiedLabel?: ReactNode;
  failedLabel?: ReactNode;
  copiedAnnouncement?: string;
  failedAnnouncement?: string;
  resetMs?: number;
  ariaLabel?: string;
  title?: string;
  variant?: ButtonVariant | "unstyled";
  size?: ButtonSize;
  className?: string;
  wrapperClassName?: string;
  children?: (status: CopyStatus) => ReactNode;
}

/**
 * Accessible copy trigger paired with a separate off-button `role="status"` live
 * region so status updates announce reliably without disabling the button.
 */
export function CopyButton({
  value,
  label = "Copy",
  copiedLabel = "Copied",
  failedLabel = "Copy failed",
  copiedAnnouncement,
  failedAnnouncement,
  resetMs = 1800,
  ariaLabel,
  title,
  variant = "secondary",
  size = "xs",
  className,
  wrapperClassName,
  children
}: CopyButtonProps) {
  const { announcement, copy, status } = useCopyToClipboard({
    resetMs,
    copiedMessage:
      copiedAnnouncement ?? (typeof copiedLabel === "string" ? copiedLabel : "Copied"),
    failedMessage:
      failedAnnouncement ??
      (typeof failedLabel === "string" ? failedLabel : "Copy failed")
  });

  const resolvedTitle =
    title ?? (status === "failed" ? "Copy failed — try selecting manually" : undefined);

  const buttonClasses =
    variant === "unstyled"
      ? cn("min-h-6 min-w-6", className)
      : buttonClassName({
          variant,
          size,
          className: cn("min-h-6 min-w-6", className)
        });

  const content = children
    ? children(status)
    : status === "copied"
      ? copiedLabel
      : status === "failed"
        ? failedLabel
        : label;

  return (
    <span className={cn("inline-flex shrink-0 items-center", wrapperClassName)}>
      <button
        aria-label={ariaLabel}
        className={buttonClasses}
        onClick={() => {
          const text = typeof value === "function" ? value() : value;
          void copy(text);
        }}
        title={resolvedTitle}
        type="button"
      >
        {content}
      </button>
      <span aria-live="polite" className="sr-only" role="status">
        {announcement}
      </span>
    </span>
  );
}
