// Folds long runs of unchanged context lines in a (whole-file) diff into
// collapsed gaps, keeping a few context lines next to each change. The server
// emits the entire file as one hunk, so every line is already present — this is
// a pure client-side fold/unfold, with per-gap reveal state driving how much of
// each gap is shown.

export interface RevealState {
  // Lines revealed from the top and bottom edges of a gap (via expand up/down).
  top: number;
  bottom: number;
}

export interface VisibleBlock {
  type: "block";
  start: number; // first visible item index (inclusive)
  end: number; // last visible item index (inclusive)
}

export interface CollapsedGap {
  type: "gap";
  index: number; // stable index among collapsible gaps (independent of reveal)
  start: number; // first still-hidden item index (inclusive)
  end: number; // last still-hidden item index (inclusive)
  hiddenTotal: number; // original hidden count, for capping + the expand label
  remaining: number; // currently hidden count (end - start + 1)
}

export type DiffSegment = VisibleBlock | CollapsedGap;

const EMPTY_REVEAL: RevealState = { top: 0, bottom: 0 };

// computeSegments returns the ordered visible blocks and collapsed gaps for a
// list of `length` items. `isContext(i)` marks foldable unchanged lines;
// `isChange(i)` marks real additions/deletions (context is only kept adjacent to
// these, not next to file edges or metadata/hunk markers). `context` is the
// number of context lines preserved beside each change. `revealedFor(gapIndex)`
// supplies how much of each gap the user has expanded.
export function computeSegments(
  length: number,
  isContext: (index: number) => boolean,
  isChange: (index: number) => boolean,
  context: number,
  revealedFor: (gapIndex: number) => RevealState
): DiffSegment[] {
  // 1. Find each maximal context run's hideable sub-range.
  const raw: { start: number; end: number }[] = [];
  let i = 0;
  while (i < length) {
    if (!isContext(i)) {
      i += 1;
      continue;
    }
    let j = i;
    while (j + 1 < length && isContext(j + 1)) {
      j += 1;
    }
    const keepTop = i > 0 && isChange(i - 1) ? context : 0;
    const keepBottom = j < length - 1 && isChange(j + 1) ? context : 0;
    const hiddenStart = i + keepTop;
    const hiddenEnd = j - keepBottom;
    if (hiddenStart <= hiddenEnd) {
      raw.push({ start: hiddenStart, end: hiddenEnd });
    }
    i = j + 1;
  }

  // 2. Apply per-gap reveal. Gap `index` stays stable even when fully revealed.
  const gaps: CollapsedGap[] = [];
  raw.forEach((gap, index) => {
    const hiddenTotal = gap.end - gap.start + 1;
    const reveal = revealedFor(index) ?? EMPTY_REVEAL;
    const top = clamp(reveal.top, 0, hiddenTotal);
    const bottom = clamp(reveal.bottom, 0, hiddenTotal - top);
    const start = gap.start + top;
    const end = gap.end - bottom;
    if (start <= end) {
      gaps.push({
        type: "gap",
        index,
        start,
        end,
        hiddenTotal,
        remaining: end - start + 1
      });
    }
  });

  // 3. Emit visible blocks interleaved with the remaining gaps, in order.
  const segments: DiffSegment[] = [];
  let cursor = 0;
  for (const gap of gaps) {
    if (cursor <= gap.start - 1) {
      segments.push({ type: "block", start: cursor, end: gap.start - 1 });
    }
    segments.push(gap);
    cursor = gap.end + 1;
  }
  if (cursor <= length - 1) {
    segments.push({ type: "block", start: cursor, end: length - 1 });
  }
  return segments;
}

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}
