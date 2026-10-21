import { describe, expect, it } from "vitest";

import { blameLines, codeLineCount } from "./blame";

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
