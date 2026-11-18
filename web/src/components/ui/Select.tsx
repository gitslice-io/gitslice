import { forwardRef, type SelectHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export type SelectSize = "sm" | "md";

const SIZE_CLASSES: Record<SelectSize, string> = {
  sm: "h-8 px-2 text-xs",
  md: "h-9 px-3 text-sm"
};

export interface SelectClassOptions {
  size?: SelectSize;
  className?: string;
}

export function selectClassName({
  size = "md",
  className
}: SelectClassOptions = {}): string {
  return cn(
    "min-w-0 rounded-md border border-slate-300 bg-white text-zinc-950 outline-none transition focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:bg-slate-50 disabled:text-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:focus:ring-zinc-700 dark:disabled:bg-zinc-950",
    SIZE_CLASSES[size],
    className
  );
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "size"> {
  size?: SelectSize;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(
  ({ size = "md", className, ...props }, ref) => (
    <select
      className={selectClassName({ size, className })}
      ref={ref}
      {...props}
    />
  )
);

Select.displayName = "Select";
