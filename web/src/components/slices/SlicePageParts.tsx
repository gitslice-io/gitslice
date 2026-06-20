import type { ReactNode } from "react";

import type { Slice } from "../../api/types";
import { cn } from "../../lib/cn";
import { Badge, PageHeader, Surface } from "../ui";

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
    <PageHeader
      actions={actions}
      description={description}
      eyebrow={eyebrow}
      title={<span className="block break-words font-serif">{title}</span>}
    />
  );
}

interface PanelProps {
  children: ReactNode;
  className?: string;
}

export function SlicePanel({ children, className }: PanelProps) {
  return (
    <Surface
      as="section"
      className={cn(
        "p-5",
        className
      )}
      level="low"
    >
      {children}
    </Surface>
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
    <Surface
      className={cn(
        "p-4 text-sm",
        tone === "error" && "bg-rose-50 text-rose-900",
        tone === "success" && "bg-tertiary-container text-tertiary",
        tone === "neutral" && "text-on-surface-variant"
      )}
      level={tone === "neutral" ? "low" : "base"}
    >
      <p className="font-label font-semibold">{title}</p>
      {children ? <div className="mt-1 leading-6">{children}</div> : null}
    </Surface>
  );
}

export function SliceLoadingBlock() {
  return (
    <div className="space-y-4">
      <div className="h-8 w-56 animate-pulse rounded-sm bg-surface-container-high" />
      <Surface className="p-5" level="low">
        <div className="h-4 w-3/4 animate-pulse rounded-sm bg-surface-container-high" />
        <div className="mt-4 h-4 w-1/2 animate-pulse rounded-sm bg-surface-container-high" />
        <div className="mt-4 h-4 w-2/3 animate-pulse rounded-sm bg-surface-container-high" />
      </Surface>
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
          <dt className="font-label text-xs font-semibold uppercase text-on-surface-muted">
            {row.label}
          </dt>
          <dd className="mt-1 min-w-0 break-all text-sm font-medium text-on-surface">
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

export function VisibilityBadge({ visibility }: { visibility?: string }) {
  const normalized = visibility?.toLowerCase();

  return (
    <Badge variant={normalized === "public" ? "tertiary" : "neutral"}>
      {visibility || "unspecified"}
    </Badge>
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
