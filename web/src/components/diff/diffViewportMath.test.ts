import { describe, expect, it } from "vitest";

import {
  adjacentFileForShortcut,
  findFocusTargetFile,
  isFilePanelCollapsed,
  nextGapReveal,
  pickTopIntersectingId,
  resolveCurrentActiveFileId,
  retainValidIds
} from "./diffViewportMath";
import { EMPTY_REVEAL, EXPAND_CHUNK } from "./diffViewShared";
import type { DiffFile } from "./parseDiff";

function makeFile(id: string, path: string, lineCount = 10): DiffFile {
  return {
    id,
    path,
    changeKind: "modified",
    additions: lineCount,
    deletions: 0,
    lines: Array.from({ length: lineCount }, (_, idx) => ({
      kind: "add" as const,
      text: `+line ${idx}`,
      content: `line ${idx}`,
      newNumber: idx + 1
    })),
    rows: []
  };
}

describe("diffViewportMath", () => {
  it("expands gap up, down, and all without exceeding hiddenTotal", () => {
    expect(nextGapReveal(EMPTY_REVEAL, "all", 55)).toEqual({ top: 55, bottom: 0 });
    expect(nextGapReveal({ top: 5, bottom: 10 }, "up", 50)).toEqual({
      top: Math.min(40, 5 + EXPAND_CHUNK),
      bottom: 10
    });
    expect(nextGapReveal({ top: 30, bottom: 10 }, "down", 50)).toEqual({
      top: 30,
      bottom: 20
    });
  });

  it("resolves active file fallback, focus target path, and j/k navigation", () => {
    const files = [
      makeFile("f1", "a.ts"),
      makeFile("f2", "dir/b.ts"),
      makeFile("f3", "c.ts")
    ];

    expect(resolveCurrentActiveFileId(files, undefined)).toBe("f1");
    expect(resolveCurrentActiveFileId(files, "missing")).toBe("f1");
    expect(resolveCurrentActiveFileId(files, "f2")).toBe("f2");
    expect(resolveCurrentActiveFileId([], "f1")).toBeUndefined();

    expect(findFocusTargetFile(files, "/dir/b.ts")?.id).toBe("f2");
    expect(findFocusTargetFile(files, "dir/b.ts")?.id).toBe("f2");
    expect(findFocusTargetFile(files, "nope.ts")).toBeUndefined();

    expect(adjacentFileForShortcut(files, "f1", "j")?.id).toBe("f2");
    expect(adjacentFileForShortcut(files, "f1", "k")).toBeUndefined();
    expect(adjacentFileForShortcut(files, "f2", "k")?.id).toBe("f1");
    expect(adjacentFileForShortcut(files, "f3", "j")).toBeUndefined();
  });

  it("picks the topmost intersecting entry and retains valid IDs efficiently", () => {
    expect(
      pickTopIntersectingId([
        { id: "f3", isIntersecting: true, top: 240 },
        { id: "f1", isIntersecting: false, top: -10 },
        { id: "f2", isIntersecting: true, top: 80 }
      ])
    ).toBe("f2");
    expect(pickTopIntersectingId([{ id: "f1", isIntersecting: false, top: 0 }])).toBeUndefined();

    const initial = new Set(["a", "b"]);
    expect(retainValidIds(initial, new Set(["a", "b", "c"]))).toBe(initial);
    expect(retainValidIds(initial, new Set(["b", "c"]))).toEqual(new Set(["b"]));
  });

  it("respects collapse overrides, viewed state, and generated files", () => {
    const normal = makeFile("n", "src/app.ts");
    const generated = makeFile("g", "package-lock.json");

    expect(isFilePanelCollapsed(normal, {}, new Set())).toBe(false);
    expect(isFilePanelCollapsed(normal, {}, new Set(["n"]))).toBe(true);
    expect(isFilePanelCollapsed(normal, { n: false }, new Set(["n"]))).toBe(false);
    expect(isFilePanelCollapsed(generated, {}, new Set())).toBe(true);
    expect(isFilePanelCollapsed(generated, { g: false }, new Set())).toBe(false);
  });
});
