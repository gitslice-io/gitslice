import type { RevealState } from "./collapse";
import { EXPAND_CHUNK, type ExpandAction } from "./diffViewShared";
import type { DiffFile } from "./parseDiff";
import { isGeneratedFile } from "./reviewState";

export interface IntersectingEntrySample {
  id: string;
  isIntersecting: boolean;
  top: number;
}

/** Computes the updated reveal window after expanding a collapsed diff gap. */
export function nextGapReveal(
  current: RevealState,
  action: ExpandAction,
  hiddenTotal: number
): RevealState {
  if (action === "all") {
    return { top: hiddenTotal, bottom: 0 };
  }
  if (action === "up") {
    return {
      top: Math.min(hiddenTotal - current.bottom, current.top + EXPAND_CHUNK),
      bottom: current.bottom
    };
  }
  return {
    top: current.top,
    bottom: Math.min(hiddenTotal - current.top, current.bottom + EXPAND_CHUNK)
  };
}

/** Resolves the active file index and fallback ID (first file when unset/missing). */
export function resolveCurrentActiveFileId(
  files: readonly DiffFile[],
  activeId: string | undefined
): string | undefined {
  const activeFileIndex = files.findIndex((file) => file.id === activeId);
  return files[activeFileIndex >= 0 ? activeFileIndex : 0]?.id;
}

/** Matches a `?file=` query path against the parsed diff file list. */
export function findFocusTargetFile(
  files: readonly DiffFile[],
  focusFilePath: string
): DiffFile | undefined {
  if (!focusFilePath || !files.length) {
    return undefined;
  }
  const wanted = focusFilePath.replace(/^\/+/, "");
  return files.find((file) => file.path.replace(/^\/+/, "") === wanted);
}

/** Returns the target DiffFile for `j` (next) / `k` (previous) keyboard shortcuts. */
export function adjacentFileForShortcut(
  files: readonly DiffFile[],
  currentActiveId: string | undefined,
  key: "j" | "k"
): DiffFile | undefined {
  const index = Math.max(
    0,
    files.findIndex((file) => file.id === currentActiveId)
  );
  return files[index + (key === "j" ? 1 : -1)];
}

/** Picks the topmost intersecting entry ID for scroll-spy updates. */
export function pickTopIntersectingId(
  entries: readonly IntersectingEntrySample[]
): string | undefined {
  const visible = entries
    .filter((entry) => entry.isIntersecting && Boolean(entry.id))
    .sort((left, right) => left.top - right.top);
  return visible[0]?.id;
}

/** Returns a set retaining only valid IDs, or the original Set if unchanged. */
export function retainValidIds(
  current: Set<string>,
  validIds: ReadonlySet<string>
): Set<string> {
  const next = new Set<string>();
  current.forEach((id) => {
    if (validIds.has(id)) {
      next.add(id);
    }
  });
  return next.size === current.size ? current : next;
}

/** Determines whether a file panel starts or stays collapsed. */
export function isFilePanelCollapsed(
  file: DiffFile,
  collapseOverride: Readonly<Record<string, boolean>>,
  viewedIds: ReadonlySet<string>
): boolean {
  return collapseOverride[file.id] ?? (viewedIds.has(file.id) || isGeneratedFile(file));
}
