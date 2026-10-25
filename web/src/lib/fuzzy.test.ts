import { describe, expect, it } from "vitest";

import { fuzzyMatch, rankPaths } from "./fuzzy";

const files = [
  "/gitslice/gitslice/README.md",
  "/gitslice/gitslice/web/src/routes/ChangesetsPage.tsx",
  "/gitslice/gitslice/web/src/routes/ChangesetDetailPage.tsx",
  "/gitslice/gitslice/web/src/routes/changesets-list/filters.ts",
  "/gitslice/gitslice/web/src/components/diff/DiffViewer.tsx",
  "/gitslice/gitslice/web/src/components/diff/DiffFilePanel.tsx",
  "/gitslice/gitslice/internal/postgres/changeset_store.go",
  "/gitslice/gitslice/service/changeset.go",
  "/gitslice/gitslice/service/changeset_list_test.go",
  "/gitslice/gitslice/web/package.json",
  "/gitslice/gitslice/package-lock.json"
];

const top = (query: string) => rankPaths(query, files, 3).map((match) => match.path.split("/").pop());

describe("fuzzy matching", () => {
  it("needs every letter, in order", () => {
    expect(fuzzyMatch("rdm", "/a/README.md")).not.toBeNull();
    expect(fuzzyMatch("mdr", "/a/README.md")).toBeNull();
    expect(fuzzyMatch("xyz", "/a/README.md")).toBeNull();
    expect(fuzzyMatch("", "/a/README.md")).toBeNull();
    expect(fuzzyMatch("   ", "/a/README.md")).toBeNull();
    // Longer than the path can hold.
    expect(fuzzyMatch("readme.mdx", "/a/README.md")).toBeNull();
  });

  it("ignores case and spaces in the query", () => {
    expect(fuzzyMatch("read me", "/a/README.md")?.indexes).toEqual(fuzzyMatch("README", "/a/README.md")?.indexes);
    expect(fuzzyMatch("readme", "/a/README.md")).not.toBeNull();
  });

  it("says which letters matched, as positions in the whole path", () => {
    const path = "/web/src/DiffViewer.tsx";
    const match = fuzzyMatch("dv", path)!;
    expect(match.indexes.map((index) => path[index])).toEqual(["D", "V"]);
    const crossing = fuzzyMatch("srcdiff", path)!;
    expect(crossing.indexes.map((index) => path[index].toLowerCase()).join("")).toBe("srcdiff");
  });

  it("prefers a file's name over its folders", () => {
    // "diff" is a folder of two files and the start of one name.
    const ranked = rankPaths("diff", ["/web/src/components/diff/index.ts", "/web/src/DiffViewer.tsx"], 2);
    expect(ranked[0].path).toBe("/web/src/DiffViewer.tsx");
  });

  it("finds a file by the starts of its words and by its capitals", () => {
    expect(top("cspage")).toContain("ChangesetsPage.tsx");
    expect(top("dvw")[0]).toBe("DiffViewer.tsx");
    expect(top("csdp")[0]).toBe("ChangesetDetailPage.tsx");
    expect(top("chgstore")[0]).toBe("changeset_store.go");
    expect(top("pkglock")[0]).toBe("package-lock.json");
  });

  it("puts an exact name before longer ones", () => {
    expect(top("changeset.go")[0]).toBe("changeset.go");
    expect(top("readme")[0]).toBe("README.md");
    expect(top("filters")[0]).toBe("filters.ts");
  });

  it("matches across folders when the name alone does not", () => {
    expect(top("routes/filters")[0]).toBe("filters.ts");
    expect(top("postgres/store")[0]).toBe("changeset_store.go");
  });

  it("ranks consecutive letters above scattered ones, and shorter paths above longer ones on a tie", () => {
    const ranked = rankPaths("abc", ["/x/a_b_c.ts", "/x/abc.ts", "/x/abc.tsx"], 3).map((match) => match.path);
    expect(ranked).toEqual(["/x/abc.ts", "/x/abc.tsx", "/x/a_b_c.ts"]);
  });

  it("returns at most the limit, best first, and nothing when nothing matches", () => {
    expect(rankPaths("e", files, 4)).toHaveLength(4);
    const scores = rankPaths("page", files).map((match) => match.score);
    expect([...scores].sort((a, b) => b - a)).toEqual(scores);
    expect(rankPaths("qqq", files)).toEqual([]);
  });

  it("stays fast over a large tree", () => {
    const many = Array.from({ length: 20000 }, (_, i) => `/proj/src/module${i % 97}/component_${i}/index_file_${i}.ts`);
    const started = performance.now();
    const ranked = rankPaths("cmpidx", many, 50);
    expect(ranked).toHaveLength(50);
    expect(performance.now() - started).toBeLessThan(1500);
  });
});
