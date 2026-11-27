import { forwardRef, type TextareaHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  monospace?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ monospace = false, className, ...props }, ref) => (
    <textarea
      className={cn(
        "min-w-0 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-zinc-950 outline-none transition placeholder:text-slate-400 focus:border-zinc-500 focus:ring-2 focus:ring-zinc-200 disabled:bg-slate-50 disabled:text-slate-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:placeholder:text-zinc-500 dark:focus:ring-zinc-700 dark:disabled:bg-zinc-950",
        monospace && "font-mono",
        className
      )}
      ref={ref}
      {...props}
    />
  )
);

Textarea.displayName = "Textarea";
