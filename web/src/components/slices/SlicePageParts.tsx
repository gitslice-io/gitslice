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

export function VisibilityIcon({ visibility }: { visibility?: string }) {
  const value = visibility || "unspecified";
  const isPublic = value === "public";

  return (
    <span
      aria-label={`${value} slice`}
      className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md text-slate-500"
      role="img"
      title={`${value} slice`}
    >
      {isPublic ? <PublicSliceIcon /> : <PrivateSliceIcon />}
    </span>
  );
}

function PublicSliceIcon() {
  return (
    <svg
      aria-hidden
      className="h-4 w-4"
      fill="none"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.8"
      viewBox="0 0 24 24"
    >
      <circle cx="12" cy="12" r="8.5" />
      <path d="M3.5 12h17" />
      <path d="M12 3.5c2.2 2.3 3.4 5.1 3.4 8.5S14.2 18.2 12 20.5" />
      <path d="M12 3.5C9.8 5.8 8.6 8.6 8.6 12s1.2 6.2 3.4 8.5" />
    </svg>
  );
}

function PrivateSliceIcon() {
  return (
    <svg
      aria-hidden
      className="h-4 w-4"
      fill="none"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.8"
      viewBox="0 0 24 24"
    >
      <rect height="9" rx="2" width="14" x="5" y="10.5" />
      <path d="M8 10.5V8a4 4 0 0 1 8 0v2.5" />
      <path d="M12 14v2" />
    </svg>
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
