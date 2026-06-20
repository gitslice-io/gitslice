import { forwardRef, type InputHTMLAttributes } from "react";

import { cn } from "../../lib/cn";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  error?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, error = false, type = "text", ...props }, ref) => {
    const ariaInvalid = error ? true : props["aria-invalid"];

    return (
      <input
        {...props}
        aria-invalid={ariaInvalid}
        className={cn(
          "h-10 w-full rounded-sm bg-white px-3 text-sm text-on-surface outline-none ring-1 ring-outline-variant/15 transition placeholder:text-on-surface-muted focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:bg-surface-container-high disabled:text-on-surface-muted aria-[invalid=true]:ring-2 aria-[invalid=true]:ring-rose-700/40",
          className
        )}
        ref={ref}
        type={type}
      />
    );
  }
);

Input.displayName = "Input";
