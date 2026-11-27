import type { ReactNode } from "react";

import { cn } from "../../lib/cn";

export type AlertTone = "neutral" | "error" | "success";

export interface AlertProps {
  children?: ReactNode;
  className?: string;
  title?: string;
  tone?: AlertTone;
}

const toneClasses: Record<AlertTone, string> = {
  error:
    "border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 text-rose-900 dark:text-rose-200",
  success:
    "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-900 dark:text-emerald-200",
  neutral:
    "border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-700 dark:text-zinc-300"
};

export function Alert({
  children,
  className,
  title,
  tone = "neutral"
}: AlertProps) {
  return (
    <div
      className={cn("rounded-lg border p-4 text-sm", toneClasses[tone], className)}
      role={tone === "error" ? "alert" : tone === "success" ? "status" : undefined}
    >
      {title ? <p className="font-semibold">{title}</p> : null}
      {children ? <div className={cn(title && "mt-1", "leading-6")}>{children}</div> : null}
    </div>
  );
}
