import { test } from "node:test";
import assert from "node:assert/strict";
import { lcsPairs, merge3, splitLines, unifiedDiff } from "../src/merge.ts";

test("lcsPairs finds a longest common subsequence", () => {
  const a = splitLines("a\nb\nc\nd\n");
  const b = splitLines("a\nx\nc\nd\ny\n");
  const pairs = lcsPairs(a, b);
  assert.deepEqual(pairs, [[0, 0], [2, 2], [3, 3]]);
});

test("merge3 combines edits to different parts of a file", () => {
  const base = "function subtotal() {\n  return 1;\n}\n\nconst TAX = 0.08;\n\nfunction shipping() {\n  return 5;\n}\n";
  const ours = base.replace("return 1;", "return items.reduce(add, 0);");
  const theirs = base.replace("return 5;", "return weight > 10 ? 9 : 5;");
  const merged = merge3(base, ours, theirs);
  assert.equal(merged.clean, true);
  if (merged.clean) {
    assert.match(merged.text, /items\.reduce/);
    assert.match(merged.text, /weight > 10/);
  }
});

test("merge3 reports overlapping edits as conflicts", () => {
  const base = "const TAX = 0.08;\n";
  const merged = merge3(base, "const TAX = 0.0825;\n", "const TAX = 0.09;\n");
  assert.equal(merged.clean, false);
  if (!merged.clean) assert.deepEqual(merged.conflicts[0].ours, ["const TAX = 0.0825;\n"]);
});

test("merge3 accepts identical changes and one-sided changes", () => {
  assert.deepEqual(merge3("a\n", "b\n", "b\n"), { clean: true, text: "b\n" });
  assert.deepEqual(merge3("a\nb\n", "a\nb\nc\n", "a\nb\n"), { clean: true, text: "a\nb\nc\n" });
  assert.deepEqual(merge3("", "x\n", ""), { clean: true, text: "x\n" });
});

test("unifiedDiff renders hunks with context", () => {
  const before = Array.from({ length: 20 }, (_, i) => `line ${i}\n`).join("");
  const after = before.replace("line 3\n", "line three\n").replace("line 17\n", "");
  const diff = unifiedDiff("f.txt", before, after);
  assert.match(diff, /^--- a\/f\.txt\n\+\+\+ b\/f\.txt\n@@ -1,7 \+1,7 @@/);
  assert.match(diff, /-line 3\n\+line three/);
  assert.match(diff, /-line 17/);
  assert.equal((diff.match(/^@@/gm) ?? []).length, 2);
});

test("merge3 and lcsPairs handle large files with small edits and fall back cleanly to conflict when edit bounds are exceeded", () => {
  // 10,000-line file with small disjoint edits at top and bottom trims prefix/suffix and merges cleanly!
  const bigLines = Array.from({ length: 10_000 }, (_, i) => `line ${i}\n`);
  const base = bigLines.join("");
  const oursLines = [...bigLines];
  oursLines[5] = "ours line 5\n";
  const theirsLines = [...bigLines];
  theirsLines[9_990] = "theirs line 9990\n";
  const cleanBig = merge3(base, oursLines.join(""), theirsLines.join(""));
  assert.equal(cleanBig.clean, true);

  // Huge completely divergent files fall back cleanly to conflict without OOMing.
  const hugeA = Array.from({ length: 5_000 }, (_, i) => `alpha ${i}\n`).join("");
  const hugeB = Array.from({ length: 5_000 }, (_, i) => `beta ${i}\n`).join("");
  const hugeC = Array.from({ length: 5_000 }, (_, i) => `gamma ${i}\n`).join("");
  const boundedConflict = merge3(hugeA, hugeB, hugeC);
  assert.equal(boundedConflict.clean, false);
  if (!boundedConflict.clean) {
    assert.equal(boundedConflict.conflicts.length, 1);
    assert.ok(boundedConflict.conflicts[0].ours.length <= 80);
  }
});
