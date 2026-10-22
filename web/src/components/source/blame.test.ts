import { describe, expect, it } from "vitest";

import { blameLines, changeOrder, codeLineCount, commitBands } from "./blame";

describe("blameLines", () => {
  const blame = {
    lineCount: 5,
    ranges: [
      { startLine: 1, lineCount: 2, commitId: "c1" },
      { startLine: 3, lineCount: 1, commitId: "c2" },
      { startLine: 4, lineCount: 2, commitId: "c1" }
    ],
    commits: [
      { id: "c1", author: "alice", message: "first" },
      { id: "c2", author: "bob", message: "second" }
    ]
  };

  it("gives every line its commit, marking where each run starts", () => {
    const lines = blameLines(blame, 5);
    expect(lines.map((line) => line.commitId)).toEqual(["c1", "c1", "c2", "c1", "c1"]);
    expect(lines.map((line) => line.first)).toEqual([true, false, true, true, false]);
    expect(lines.map((line) => line.run)).toEqual([0, 0, 1, 2, 2]);
    expect(lines[2].commit?.author).toBe("bob");
  });

  it("knows each line's place in its run and its change's order", () => {
    const dated = {
      ...blame,
      commits: [
        { id: "c1", author: "alice", message: "first", createdAt: "2026-10-05T00:00:00Z" },
        { id: "c2", author: "bob", message: "second", createdAt: "2026-10-01T00:00:00Z" }
      ]
    };
    const lines = blameLines(dated, 5);
    expect(lines.map((line) => line.offset)).toEqual([0, 1, 0, 0, 1]);
    expect(lines.map((line) => line.length)).toEqual([2, 2, 1, 2, 2]);
    // c2 is older, though its lines come later in the file.
    expect(lines.map((line) => line.order)).toEqual([2, 2, 1, 2, 2]);
    expect(lines.every((line) => line.total === 2)).toBe(true);
  });

  it("keeps lines no run covers, and ignores runs past the end", () => {
    const lines = blameLines({ ranges: [{ startLine: 2, lineCount: 10, commitId: "c9" }] }, 3);
    expect(lines).toHaveLength(3);
    expect(lines[0].commitId).toBe("");
    expect(lines[1].commitId).toBe("c9");
    expect(lines[2].commit).toBeUndefined();
  });
});

describe("codeLineCount", () => {
  it("counts lines the way a blame does", () => {
    expect(codeLineCount("")).toBe(0);
    expect(codeLineCount("a")).toBe(1);
    expect(codeLineCount("a\nb")).toBe(2);
    expect(codeLineCount("a\nb\n")).toBe(2);
    expect(codeLineCount("a\r\nb\r\n")).toBe(2);
    expect(codeLineCount("\n")).toBe(1);
  });
});

describe("changeOrder", () => {
  it("numbers commits oldest first, undated ones before dated, ties in file order", () => {
    const order = changeOrder({
      ranges: [
        { startLine: 1, lineCount: 1, commitId: "new" },
        { startLine: 2, lineCount: 1, commitId: "nodate" },
        { startLine: 3, lineCount: 1, commitId: "old" },
        { startLine: 4, lineCount: 1, commitId: "tie" },
        { startLine: 5, lineCount: 1, commitId: "new" }
      ],
      commits: [
        { id: "new", createdAt: "2026-10-09T00:00:00Z" },
        { id: "old", createdAt: "2026-01-01T00:00:00Z" },
        { id: "tie", createdAt: "2026-01-01T00:00:00Z" },
        { id: "unused", createdAt: "2020-01-01T00:00:00Z" }
      ]
    });
    expect([...order.entries()]).toEqual([
      ["nodate", 1],
      ["old", 2],
      ["tie", 3],
      ["new", 4]
    ]);
  });
});

describe("commitBands", () => {
  const blame = {
    ranges: [
      { startLine: 1, lineCount: 2, commitId: "c1" },
      { startLine: 3, lineCount: 1, commitId: "c2" },
      { startLine: 4, lineCount: 2, commitId: "c1" },
      { startLine: 6, lineCount: 1, commitId: "c3" }
    ]
  };

  it("gives each separate stretch of a commit's lines its own band, 0-based", () => {
    const lines = blameLines(blame, 6);
    expect(commitBands(lines, "c1")).toEqual([
      { end: 1, start: 0 },
      { end: 4, start: 3 }
    ]);
    expect(commitBands(lines, "c2")).toEqual([{ end: 2, start: 2 }]);
    expect(commitBands(lines, "c3")).toEqual([{ end: 5, start: 5 }]);
  });

  it("has none for a commit that owns no line, or no commit", () => {
    const lines = blameLines(blame, 6);
    expect(commitBands(lines, "nobody")).toEqual([]);
    expect(commitBands(lines, "")).toEqual([]);
  });
});
