import { computeSegments, type DiffSegment, type RevealState } from "./collapse";
import { type DiffFile, type DiffLine, type DiffRow } from "./parseDiff";

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

// The segments a file shows in unified view: runs of lines, and the folded
// gaps between them. What the renderer draws and what a placeholder is sized
// from, so the two cannot drift apart.
export function unifiedSegments(
  fileId: string,
  lines: readonly DiffLine[],
  revealed: Record<string, RevealState>
): DiffSegment[] {
  return computeSegments(
    lines.length,
    (index) => lines[index].kind === "context",
    (index) => lines[index].kind === "add" || lines[index].kind === "del",
    CONTEXT_LINES,
    (gapIndex) => revealed[gapKey(fileId, "unified", gapIndex)] ?? EMPTY_REVEAL
  );
}

export function splitSegments(
  fileId: string,
  rows: readonly DiffRow[],
  revealed: Record<string, RevealState>
): DiffSegment[] {
  return computeSegments(
    rows.length,
    (index) => rows[index].kind === "context",
    (index) =>
      rows[index].kind === "add" ||
      rows[index].kind === "del" ||
      rows[index].kind === "replace",
    CONTEXT_LINES,
    (gapIndex) => revealed[gapKey(fileId, "split", gapIndex)] ?? EMPTY_REVEAL
  );
}

// Heights, in pixels, of what a mounted body draws: a line (leading-4), a
// split row (the line and its top border), a fold's expand bar, and the
// body's vertical padding.
const unifiedLineHeight = 16;
const splitRowHeight = 17;
const gapBarHeight = 26;
const bodyPadding = 12;

// The height a file's body will have once it mounts, for the placeholder that
// holds its place while it is off screen. It counts the lines the body will
// show (the unchanged runs folded) rather than every line of the file, so the
// page is about as tall before the bodies load as after. Lines that wrap make
// the real body a little taller.
export function estimatedDiffBodyHeight(file: DiffFile, viewMode: ViewMode) {
  const unified = viewMode === "unified";
  const segments = unified
    ? unifiedSegments(file.id, file.lines, {})
    : splitSegments(file.id, file.rows, {});
  const rowHeight = unified ? unifiedLineHeight : splitRowHeight;
  let height = bodyPadding;
  for (const segment of segments) {
    height +=
      segment.type === "gap"
        ? gapBarHeight
        : (segment.end - segment.start + 1) * rowHeight;
  }
  return Math.max(minDiffBodyPlaceholderHeight, height);
}
export const largeDiffLineLimit = 5000;

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}
