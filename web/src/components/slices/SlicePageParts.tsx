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
    <div className="border-b border-slate-200 pb-5">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div className="min-w-0">
          <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            {eyebrow}
          </p>
          <h1 className="mt-2 break-words text-xl font-semibold tracking-normal text-zinc-950 sm:text-2xl">
            {title}
          </h1>
          {description ? (
            <p className="mt-2 max-w-2xl text-sm leading-6 text-slate-600">
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
        "rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50",
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
          "border-rose-200 bg-rose-50 text-rose-900",
        tone === "success" &&
          "border-emerald-200 bg-emerald-50 text-emerald-900",
        tone === "neutral" &&
          "border-slate-200 bg-white text-slate-700"
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
      <div className="h-8 w-56 animate-pulse rounded-md bg-slate-200" />
      <div className="rounded-lg border border-slate-200 bg-white p-5">
        <div className="h-4 w-3/4 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 h-4 w-1/2 animate-pulse rounded bg-slate-200" />
        <div className="mt-4 h-4 w-2/3 animate-pulse rounded bg-slate-200" />
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
          <dt className="text-xs font-semibold uppercase tracking-normal text-slate-500">
            {row.label}
          </dt>
          <dd className="mt-1 min-w-0 break-all text-sm font-medium text-zinc-950">
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

export function VisibilityBadge({ visibility }: { visibility?: string }) {
  return (
    <span className="inline-flex items-center rounded-md border border-slate-200 bg-slate-50 px-2 py-1 text-xs font-semibold text-slate-700">
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
    return `${account}/${slug}`;
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
