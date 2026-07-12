import type { ReactNode } from "react";

import type { Slice } from "../../api/types";
import { cn } from "../../lib/cn";

interface PageHeaderProps {
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}

export function SlicePageHeader({
  eyebrow = "Gitslice Web",
  title,
  description,
  actions
}: PageHeaderProps) {
  return (
    <div className="border-b border-slate-200 dark:border-zinc-800 pb-5">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div className="min-w-0">
          <p className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            {eyebrow}
          </p>
          <h1 className="mt-2 break-words text-xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-2xl">
            {title}
          </h1>
          {description ? (
            <p className="mt-2 max-w-2xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
              {description}
            </p>
          ) : null}
        </div>
        {actions ? <div className="min-w-0 lg:shrink-0">{actions}</div> : null}
      </div>
    </div>
  );
}

interface PanelProps {
  children: ReactNode;
  className?: string;
}

export function SlicePanel({ children, className }: PanelProps) {
  return (
    <section
      className={cn(
        "rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 shadow-sm shadow-slate-200/50 dark:shadow-black/50",
        className
      )}
    >
      {children}
    </section>
  );
}

interface NoticeProps {
  title: string;
  children?: ReactNode;
  tone?: "neutral" | "error" | "success";
}

export function SliceNotice({
  title,
  children,
  tone = "neutral"
}: NoticeProps) {
  return (
    <div
      className={cn(
        "rounded-lg border p-4 text-sm",
        tone === "error" &&
          "border-rose-200 dark:border-rose-900/60 bg-rose-50 dark:bg-rose-950/30 text-rose-900 dark:text-rose-200",
        tone === "success" &&
          "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-900 dark:text-emerald-200",
        tone === "neutral" &&
          "border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-700 dark:text-zinc-300"
      )}
    >
      <p className="font-semibold">{title}</p>
      {children ? <div className="mt-1 leading-6">{children}</div> : null}
    </div>
  );
}

export function SliceLoadingBlock() {
  return (
    <div className="space-y-4">
      <div className="h-8 w-56 animate-pulse rounded-md bg-slate-200 dark:bg-zinc-700" />
      <div className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <div className="h-4 w-3/4 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
        <div className="mt-4 h-4 w-1/2 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
        <div className="mt-4 h-4 w-2/3 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
      </div>
    </div>
  );
}

interface MetadataGridProps {
  rows: Array<{
    label: string;
    value: ReactNode;
  }>;
}

export function SliceMetadataGrid({ rows }: MetadataGridProps) {
  return (
    <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      {rows.map((row) => (
        <div key={row.label}>
          <dt className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
            {row.label}
          </dt>
          <dd className="mt-1 min-w-0 break-all text-sm font-medium text-zinc-950 dark:text-zinc-50">
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

export function VisibilityBadge({ visibility }: { visibility?: string }) {
  const isPublic = visibility === "public";

  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium capitalize",
        isPublic
          ? "border-emerald-200 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 text-emerald-700 dark:text-emerald-300"
          : "border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 text-slate-600 dark:text-zinc-400"
      )}
    >
      <span
        aria-hidden
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          isPublic ? "bg-emerald-500" : "bg-slate-400"
        )}
      />
      {visibility || "unspecified"}
    </span>
  );
}

export function sliceDisplayName(slice?: Slice) {
  if (!slice) {
    return "Unknown slice";
  }

  const account = slice.ref?.account;
  const slug = slice.ref?.slice;

  if (account && slug) {
    return `${account}:${slug}`;
  }

  return slug || slice.id || "Unknown slice";
}

export function formatPathPreview(paths: string[], max = 2) {
  if (!paths.length) {
    return "No paths";
  }

  const visiblePaths = paths.slice(0, max).join(", ");
  const remaining = paths.length - max;

  return remaining > 0 ? `${visiblePaths} and ${remaining} more` : visiblePaths;
}

export function getErrorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message;
  }

  return "Something went wrong.";
}
