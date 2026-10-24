import { describe, expect, it } from "vitest";

import {
  estimatedDiffBodyHeight,
  minDiffBodyPlaceholderHeight,
  splitSegments,
  unifiedSegments
} from "./diffViewShared";
import { parseDiff } from "./parseDiff";

// The server sends a whole file as one hunk, so a one-line change in a long
// file arrives with every other line as context.
function wholeFileDiff(lines: number, changeAt: number[]) {
  const body = Array.from({ length: lines }, (_, i) =>
    changeAt.includes(i) ? `-old ${i}\n+new ${i}` : ` line ${i}`
  );
  return [
    "diff --git a/big.ts b/big.ts",
    "--- a/big.ts",
    "+++ b/big.ts",
    `@@ -1,${lines} +1,${lines} @@`,
    ...body,
    ""
  ].join("\n");
}

describe("estimatedDiffBodyHeight", () => {
  it("sizes a placeholder from the folded lines, not from every line of the file", () => {
    const [file] = parseDiff(wholeFileDiff(1000, [500]), ["big.ts"]);
    // About 1,000 lines in the file: the old estimate was 16px a line.
    expect(file.lines.length).toBeGreaterThan(1000);

    const unified = estimatedDiffBodyHeight(file, "unified");
    const split = estimatedDiffBodyHeight(file, "split");
    // Three context lines either side of one change, two folds between the
    // file's edges and them: a few hundred pixels at most.
    expect(unified).toBeLessThan(500);
    expect(split).toBeLessThan(500);
    expect(unified).toBeGreaterThan(minDiffBodyPlaceholderHeight);
  });

  it("matches what the body draws: lines shown at 16px (17px split) and a bar per fold", () => {
    const [file] = parseDiff(wholeFileDiff(1000, [500]), ["big.ts"]);
    const lines = unifiedSegments(file.id, file.lines, {});
    const shown = lines.reduce((n, s) => n + (s.type === "block" ? s.end - s.start + 1 : 0), 0);
    const folds = lines.filter((s) => s.type === "gap").length;
    expect(estimatedDiffBodyHeight(file, "unified")).toBe(12 + shown * 16 + folds * 26);

    const rows = splitSegments(file.id, file.rows, {});
    const shownRows = rows.reduce((n, s) => n + (s.type === "block" ? s.end - s.start + 1 : 0), 0);
    const splitFolds = rows.filter((s) => s.type === "gap").length;
    expect(estimatedDiffBodyHeight(file, "split")).toBe(12 + shownRows * 17 + splitFolds * 26);
  });

  it("grows with the changes and keeps a floor for tiny or empty files", () => {
    const few = parseDiff(wholeFileDiff(1000, [100]), ["big.ts"])[0];
    const many = parseDiff(wholeFileDiff(1000, [100, 300, 500, 700, 900]), ["big.ts"])[0];
    expect(estimatedDiffBodyHeight(many, "unified")).toBeGreaterThan(estimatedDiffBodyHeight(few, "unified"));

    const empty = parseDiff("diff --git a/x b/x\nBinary files a/x and b/x differ\n", ["x"])[0];
    expect(estimatedDiffBodyHeight(empty, "unified")).toBeGreaterThanOrEqual(minDiffBodyPlaceholderHeight);
  });
});
