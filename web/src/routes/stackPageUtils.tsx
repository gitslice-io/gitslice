import type { ReactNode } from "react";

import type {
  Changeset,
  ChangesetStack,
  ChangesetStackEntry,
  Patchset,
  SliceRef
} from "../api/types";
import { Badge, Card, buttonClassName, type BadgeVariant } from "../components/ui";
import { cn } from "../lib/cn";
import { shortChangesetId, shortHash } from "../lib/objectId";

export const primaryButtonClass = buttonClassName({ variant: "primary" });

export const secondaryButtonClass = buttonClassName({ variant: "secondary" });

export const dangerButtonClass = buttonClassName({
  className: "bg-rose-100 text-rose-800 hover:bg-rose-200",
  variant: "secondary"
});

export const nativeControlClassName =
  "h-10 w-full rounded-sm bg-white px-3 text-sm text-on-surface outline-none ring-1 ring-outline-variant/15 transition placeholder:text-on-surface-muted focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:bg-surface-container-high disabled:text-on-surface-muted";

export const nativeTextareaClassName =
  "w-full rounded-sm bg-white px-3 py-2 text-sm text-on-surface outline-none ring-1 ring-outline-variant/15 transition placeholder:text-on-surface-muted focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:bg-surface-container-high disabled:text-on-surface-muted";

export function StackNotice({
  children,
  title,
  tone = "neutral"
}: {
  children?: ReactNode;
  title: string;
  tone?: "neutral" | "error" | "success";
}) {
  return (
    <Card
      className={cn(
        "text-sm",
        tone === "error" && "bg-rose-100 text-rose-900",
        tone === "success" && "bg-primary/10 text-primary"
      )}
      level={tone === "neutral" ? "low" : "base"}
      padding="sm"
    >
      <p className="font-semibold">{title}</p>
      {children ? <div className="mt-1 leading-6">{children}</div> : null}
    </Card>
  );
}

export function StackLoadingBlock() {
  return (
    <div className="space-y-4">
      <div className="h-8 w-56 animate-pulse rounded-sm bg-surface-container-highest" />
      <Card level="low" padding="md">
        <div className="h-4 w-3/4 animate-pulse rounded-sm bg-surface-container-highest" />
        <div className="mt-4 h-4 w-1/2 animate-pulse rounded-sm bg-surface-container-highest" />
        <div className="mt-4 h-4 w-2/3 animate-pulse rounded-sm bg-surface-container-highest" />
      </Card>
    </div>
  );
}

export function parseSliceSearch(value: unknown): Required<SliceRef> | null {
  if (typeof value !== "string") {
    return null;
  }

  const trimmed = value.trim();
  const sep = trimmed.includes(":") ? ":" : "/";
  const index = trimmed.indexOf(sep);

  if (index <= 0) {
    return null;
  }

  const account = trimmed.slice(0, index).trim();
  const slice = trimmed.slice(index + 1).trim();

  if (!account || !slice) {
    return null;
  }

  return { account, slice };
}

export function sliceRefLabel(ref?: SliceRef | null) {
  if (!ref?.account || !ref.slice) {
    return "";
  }
  return `${ref.account}:${ref.slice}`;
}

export function shortStackId(id?: string | null) {
  if (!id) {
    return "";
  }
  return id.replace(/^stk_/, "").slice(0, 10);
}

export function stackDisplayName(stack?: ChangesetStack | null) {
  if (!stack) {
    return "Unknown dependency tree";
  }
  return stack.title || shortStackId(stack.id) || stack.id || "Untitled dependency tree";
}

export function changesetLabel(changeset?: Changeset | null) {
  if (!changeset) {
    return "changeset";
  }
  if (changeset.handle) {
    return changeset.handle;
  }
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "changeset";
}

export function entryLabel(entry?: ChangesetStackEntry | null) {
  if (!entry) {
    return "changeset";
  }
  return changesetLabel(entry.changeset ?? { id: entry.changesetId });
}

export function entryTitle(entry?: ChangesetStackEntry | null) {
  return entry?.changeset?.title || "Untitled changeset";
}

export function sortedStackEntries(stack?: ChangesetStack | null) {
  return [...(stack?.entries ?? [])].sort((left, right) => {
    const leftOrder = numericValue(left.displayOrder);
    const rightOrder = numericValue(right.displayOrder);

    if (leftOrder !== rightOrder) {
      return leftOrder - rightOrder;
    }

    const leftSibling = numericValue(left.siblingOrder);
    const rightSibling = numericValue(right.siblingOrder);

    if (leftSibling !== rightSibling) {
      return leftSibling - rightSibling;
    }

    return (left.changesetId || "").localeCompare(right.changesetId || "");
  });
}

export function currentPatchset(changeset?: Changeset | null): Patchset | null {
  const patchsets = changeset?.patchsets ?? [];
  if (!patchsets.length) {
    return null;
  }
  const currentId = changeset?.currentPatchsetId;
  const byId = currentId
    ? patchsets.find((patchset) => patchset.id === currentId)
    : undefined;
  if (byId) {
    return byId;
  }
  return [...patchsets].sort(
    (left, right) => numericValue(right.number) - numericValue(left.number)
  )[0];
}

export function currentPatchsetNumber(changeset?: Changeset | null) {
  const number = changeset?.currentPatchsetNumber ?? currentPatchset(changeset)?.number;
  return number === undefined || number === "" ? "" : String(number);
}

export function changedPathCount(changeset?: Changeset | null) {
  const current = currentPatchset(changeset);
  return changeset?.affectedPaths?.length ?? current?.changedPaths?.length ?? 0;
}

export function conflictCount(changeset?: Changeset | null) {
  return currentPatchset(changeset)?.conflicts?.length ?? 0;
}

export function childCountMap(entries: ChangesetStackEntry[]) {
  const counts = new Map<string, number>();
  for (const entry of entries) {
    const parent = entry.parentChangesetId || entry.changeset?.parentChangesetId;
    if (!parent) {
      continue;
    }
    counts.set(parent, (counts.get(parent) ?? 0) + 1);
  }
  return counts;
}

export function entryDepth(entry?: ChangesetStackEntry | null) {
  return numericValue(entry?.depth ?? entry?.changeset?.stackDepth);
}

export function entryByChangesetId(
  entries: ChangesetStackEntry[],
  changesetId: string
) {
  return entries.find((entry) => entry.changesetId === changesetId) ?? null;
}

export function parentEntry(
  entries: ChangesetStackEntry[],
  entry?: ChangesetStackEntry | null
) {
  const parentId = entry?.parentChangesetId || entry?.changeset?.parentChangesetId;
  return parentId ? entryByChangesetId(entries, parentId) : null;
}

export function affectedSubtreeEntries({
  entries,
  includeSiblings,
  startChangesetId
}: {
  entries: ChangesetStackEntry[];
  includeSiblings: boolean;
  startChangesetId: string;
}) {
  const sorted = [...entries].sort(
    (left, right) => numericValue(left.displayOrder) - numericValue(right.displayOrder)
  );
  const start = startChangesetId
    ? entryByChangesetId(sorted, startChangesetId)
    : sorted.find((entry) => !entry.parentChangesetId) ?? sorted[0] ?? null;
  if (!start) {
    return [];
  }

  const roots = includeSiblings
    ? sorted.filter(
        (entry) =>
          (entry.parentChangesetId || "") === (start.parentChangesetId || "")
      )
    : [start];
  const rootIds = new Set(roots.map((entry) => entry.changesetId).filter(Boolean));
  const selected = new Set(rootIds);
  let changed = true;

  while (changed) {
    changed = false;
    for (const entry of sorted) {
      if (entry.changesetId && selected.has(entry.changesetId)) {
        continue;
      }
      const parent = entry.parentChangesetId || "";
      if (parent && selected.has(parent) && entry.changesetId) {
        selected.add(entry.changesetId);
        changed = true;
      }
    }
  }

  return sorted.filter((entry) => entry.changesetId && selected.has(entry.changesetId));
}

export function formatCommit(commitId?: string | null) {
  return shortHash(commitId || "") || "not returned";
}

export function formatTimestamp(value?: string | null) {
  if (!value) {
    return "not returned";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

export function getErrorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

export function numericValue(value: unknown) {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}

export function isTerminalChangesetStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return (
    normalized === "submitted" ||
    normalized === "pending_publish" ||
    normalized === "published" ||
    normalized === "merged" ||
    normalized === "abandoned"
  );
}

export function StackStatusBadge({ status }: { status?: string }) {
  const label =
    status === "needs_restack"
      ? "needs update"
      : status === "needs_rebase"
        ? "needs update"
        : status || "unknown";

  return (
    <Badge className={statusClass(status)} variant={statusVariant(status)}>
      {label}
    </Badge>
  );
}

export function displaySubmitBlockedReason(reason?: string) {
  switch ((reason || "").trim()) {
    case "NeedsRestack":
    case "NeedsBaseUpdate":
      return "needs base update";
    case "BlockedOnStackParent":
    case "BlockedOnBaseChangeset":
      return "base changeset has not landed";
    default:
      return reason || "";
  }
}

export function statusClass(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "submitted":
    case "published":
    case "merged":
    case "closed":
    case "clean":
      return "bg-primary/10 text-primary";
    case "accepted":
    case "pending_publish":
    case "partial":
    case "needs_restack":
    case "needs_rebase":
      return "";
    case "blocked":
    case "failed":
    case "abandoned":
    case "conflict":
    case "conflicted":
      return "bg-rose-100 text-rose-800";
    case "draft":
    case "open":
      return "";
    default:
      return "";
  }
}

function statusVariant(status?: string): BadgeVariant {
  switch ((status || "").toLowerCase()) {
    case "submitted":
    case "published":
    case "merged":
    case "closed":
    case "clean":
      return "primary";
    case "accepted":
    case "pending_publish":
    case "partial":
    case "needs_restack":
    case "needs_rebase":
      return "tertiary";
    default:
      return "neutral";
  }
}
