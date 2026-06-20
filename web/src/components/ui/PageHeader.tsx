import { type HTMLAttributes, type ReactNode } from "react";

import { cn } from "../../lib/cn";

export interface PageHeaderProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}

export function PageHeader({
  actions,
  className,
  description,
  eyebrow,
  title,
  ...props
}: PageHeaderProps) {
  return (
    <header
      className={cn(
        "flex flex-col gap-4 py-2 sm:flex-row sm:items-end sm:justify-between",
        className
      )}
      {...props}
    >
      <div className="min-w-0">
        {eyebrow ? (
          <p className="font-label text-xs font-semibold uppercase text-tertiary">
            {eyebrow}
          </p>
        ) : null}
        <h1 className="mt-2 max-w-4xl text-3xl font-semibold leading-tight text-on-surface md:text-4xl">
          {title}
        </h1>
        {description ? (
          <p className="mt-3 max-w-2xl text-sm leading-6 text-on-surface-variant md:text-base">
            {description}
          </p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex shrink-0 flex-wrap items-center gap-2 sm:justify-end">
          {actions}
        </div>
      ) : null}
    </header>
  );
}
