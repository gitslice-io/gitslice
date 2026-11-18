import { forwardRef, type ButtonHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export type ButtonVariant = "primary" | "secondary" | "danger" | "ghost";
export type ButtonSize = "xs" | "sm" | "md";

const VARIANT_CLASSES: Record<ButtonVariant, string> = {
  primary:
    "bg-zinc-950 font-semibold text-white hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white",
  secondary:
    "border border-slate-200 bg-white font-medium text-slate-700 hover:bg-slate-50 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950",
  danger:
    "border border-slate-200 bg-white font-medium text-rose-700 hover:bg-rose-50 dark:border-zinc-800 dark:bg-zinc-900 dark:text-rose-300 dark:hover:bg-rose-950/30",
  ghost:
    "bg-transparent font-medium text-slate-600 hover:bg-slate-100 hover:text-zinc-950 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
};

const SIZE_CLASSES: Record<ButtonSize, string> = {
  xs: "px-2 py-0.5 text-xs",
  sm: "px-2.5 py-1 text-xs",
  md: "px-3 py-1.5 text-sm"
};

export interface ButtonClassOptions {
  variant?: ButtonVariant;
  size?: ButtonSize;
  className?: string;
}

export function buttonClassName({
  variant = "secondary",
  size = "sm",
  className
}: ButtonClassOptions = {}): string {
  return cn(
    "inline-flex items-center justify-center rounded-md transition active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-50",
    VARIANT_CLASSES[variant],
    SIZE_CLASSES[size],
    className
  );
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ variant = "secondary", size = "sm", className, type = "button", ...props }, ref) => (
    <button
      className={buttonClassName({ variant, size, className })}
      ref={ref}
      type={type}
      {...props}
    />
  )
);

Button.displayName = "Button";
