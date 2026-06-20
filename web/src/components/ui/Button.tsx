import { forwardRef, type ButtonHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export type ButtonVariant = "primary" | "secondary" | "tertiary";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonStyleOptions {
  variant?: ButtonVariant;
  size?: ButtonSize;
  className?: string;
}

export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    ButtonStyleOptions {}

const baseButtonClassName =
  "inline-flex items-center justify-center gap-2 rounded-sm font-label font-semibold transition duration-200 ease-out focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-surface active:translate-y-px disabled:pointer-events-none disabled:translate-y-0 disabled:cursor-not-allowed disabled:opacity-50";

const variantClassNames: Record<ButtonVariant, string> = {
  primary:
    "bg-gradient-to-r from-primary to-primary-container text-white hover:to-primary",
  secondary:
    "bg-surface-container-high text-primary hover:bg-surface-container-highest",
  tertiary:
    "bg-transparent text-primary underline-offset-4 hover:underline"
};

const sizeClassNames: Record<ButtonSize, string> = {
  sm: "h-8 px-3 text-xs",
  md: "h-10 px-4 text-sm",
  lg: "h-11 px-5 text-sm"
};

export function buttonClassName({
  variant = "primary",
  size = "md",
  className
}: ButtonStyleOptions = {}) {
  return cn(
    baseButtonClassName,
    variantClassNames[variant],
    sizeClassNames[size],
    className
  );
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  (
    { className, disabled, size = "md", type = "button", variant = "primary", ...props },
    ref
  ) => (
    <button
      className={buttonClassName({ className, size, variant })}
      disabled={disabled}
      ref={ref}
      type={type}
      {...props}
    />
  )
);

Button.displayName = "Button";
