import { type HTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export type BadgeVariant = "neutral" | "tertiary" | "primary";
export type BadgeSize = "sm" | "md";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  variant?: BadgeVariant;
  size?: BadgeSize;
}

const variantClassNames: Record<BadgeVariant, string> = {
  neutral: "bg-surface-container-high text-on-surface-variant",
  tertiary: "bg-tertiary-container text-tertiary",
  primary: "bg-primary/10 text-primary"
};

const sizeClassNames: Record<BadgeSize, string> = {
  sm: "px-2 py-1 text-[11px]",
  md: "px-2.5 py-1.5 text-xs"
};

export function Badge({
  className,
  size = "sm",
  variant = "neutral",
  ...props
}: BadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-sm font-label font-semibold leading-none",
        variantClassNames[variant],
        sizeClassNames[size],
        className
      )}
      {...props}
    />
  );
}
