import { type RevealState } from "./collapse";

// Show this many context lines next to each change; collapse the rest into
// expandable gaps. Expanding up/down reveals a chunk at a time.
export const CONTEXT_LINES = 3;
export const EXPAND_CHUNK = 20;
export const EMPTY_REVEAL: RevealState = { top: 0, bottom: 0 };

type ExpandAction = "up" | "down" | "all";
export type ExpandFn = (key: string, action: ExpandAction, hiddenTotal: number) => void;

export function gapKey(fileId: string, viewMode: ViewMode, gapIndex: number) {
  return `${fileId}:${viewMode}:${gapIndex}`;
}

export function rangeIndexes(start: number, end: number): number[] {
  const out: number[] = [];
  for (let index = start; index <= end; index += 1) {
    out.push(index);
  }
  return out;
}

export type ViewMode = "unified" | "split";

export const minDiffBodyPlaceholderHeight = 48;
export const largeDiffLineLimit = 5000;

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}
