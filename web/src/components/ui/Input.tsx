import { forwardRef, type InputHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export type InputSize = "sm" | "md";

const SIZE_CLASSES: Record<InputSize, string> = {
  sm: "h-7 px-2 text-xs",
  md: "h-9 px-3 text-sm"
};

export interface InputClassOptions {
  size?: InputSize;
  monospace?: boolean;
  className?: string;
}

export function inputClassName({
  size = "md",
  monospace = false,
  className
}: InputClassOptions = {}): string {
  return cn(
    "min-w-0 rounded-md border border-slate-300 bg-white text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:bg-slate-50 disabled:text-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:placeholder:text-zinc-500 dark:focus:ring-zinc-700 dark:disabled:bg-zinc-950",
    SIZE_CLASSES[size],
    monospace && "font-mono",
    className
  );
}

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "size"> {
  size?: InputSize;
  monospace?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ size = "md", monospace = false, className, ...props }, ref) => (
    <input
      className={inputClassName({ size, monospace, className })}
      ref={ref}
      {...props}
    />
  )
);

Input.displayName = "Input";
