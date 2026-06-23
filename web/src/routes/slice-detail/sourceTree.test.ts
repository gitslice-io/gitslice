import { describe, expect, it } from "vitest";

import {
  buildSourceTree,
  flattenTreeRows,
  ancestorDirectoryPaths,
  parentAncestorDirectoryPaths,
  parentRepositoryPath,
  repositoryPathName,
  normalizeTreePath,
  compareTreeNodes,
  compareRepositoryPaths,
  pathSearchValue,
  initialTreeExpansion,
  type DirectoryLoadResult
} from "./sourceTree";

describe("normalizeTreePath", () => {
  it("normalizes empty string", () => {
    expect(normalizeTreePath("")).toBe("");
  });

  it("normalizes slash", () => {
    expect(normalizeTreePath("/")).toBe("");
  });

  it("normalizes path without leading slash", () => {
    expect(normalizeTreePath("foo/bar")).toBe("/foo/bar");
  });

  it("normalizes path with leading slash", () => {
    expect(normalizeTreePath("/foo/bar")).toBe("/foo/bar");
  });

  it("normalizes path with trailing slash", () => {
    expect(normalizeTreePath("/foo/bar/")).toBe("/foo/bar");
  });

  it("normalizes path with multiple slashes", () => {
    expect(normalizeTreePath("//foo//bar//")).toBe("/foo/bar");
  });
});

describe("ancestorDirectoryPaths", () => {
  it("returns empty array for empty path", () => {
    expect(ancestorDirectoryPaths("")).toEqual([""]);
  });

  it("returns root for single-level path", () => {
    expect(ancestorDirectoryPaths("/foo")).toEqual(["", "/foo"]);
  });

  it("returns intermediate directories for multi-level path", () => {
    expect(ancestorDirectoryPaths("/foo/bar/baz")).toEqual([
      "",
      "/foo",
      "/foo/bar",
      "/foo/bar/baz"
    ]);
  });

  it("handles paths without leading slash", () => {
    expect(ancestorDirectoryPaths("foo/bar")).toEqual([
      "",
      "/foo",
      "/foo/bar"
    ]);
  });
});

describe("parentAncestorDirectoryPaths", () => {
  it("returns root for empty path", () => {
    expect(parentAncestorDirectoryPaths("")).toEqual([""]);
  });

  it("returns root for single-level path", () => {
    expect(parentAncestorDirectoryPaths("/foo")).toEqual([""]);
  });

  it("returns ancestors excluding the path itself", () => {
    expect(parentAncestorDirectoryPaths("/foo/bar/baz")).toEqual([
      "",
      "/foo",
      "/foo/bar"
    ]);
  });
});

describe("parentRepositoryPath", () => {
  it("returns empty string for empty path", () => {
    expect(parentRepositoryPath("")).toBe("");
  });

  it("returns empty string for single-level path", () => {
    expect(parentRepositoryPath("/foo")).toBe("");
  });

  it("returns parent path for multi-level path", () => {
    expect(parentRepositoryPath("/foo/bar")).toBe("/foo");
  });

  it("handles nested paths", () => {
    expect(parentRepositoryPath("/foo/bar/baz")).toBe("/foo/bar");
  });
});

describe("repositoryPathName", () => {
  it("returns fallback name for empty path", () => {
    expect(repositoryPathName("")).toBe("slice root");
  });

  it("returns last component for single-level path", () => {
    expect(repositoryPathName("/foo")).toBe("foo");
  });

  it("returns last component for multi-level path", () => {
    expect(repositoryPathName("/foo/bar/baz")).toBe("baz");
  });

  it("handles paths without leading slash", () => {
    expect(repositoryPathName("foo/bar")).toBe("bar");
  });
});

describe("compareTreeNodes", () => {
  it("sorts directories before files", () => {
    const dir = {
      children: new Map(),
      kind: "ENTRY_KIND_DIRECTORY" as const,
      name: "b",
      path: "/b",
      synthetic: false
    };
    const file = {
      children: new Map(),
      kind: "ENTRY_KIND_FILE" as const,
      name: "a",
      path: "/a",
      synthetic: false
    };
    expect(compareTreeNodes(dir, file)).toBe(-1);
    expect(compareTreeNodes(file, dir)).toBe(1);
  });

  it("sorts directories alphabetically", () => {
    const a = {
      children: new Map(),
      kind: "ENTRY_KIND_DIRECTORY" as const,
      name: "a",
      path: "/a",
      synthetic: false
    };
    const b = {
      children: new Map(),
      kind: "ENTRY_KIND_DIRECTORY" as const,
      name: "b",
      path: "/b",
      synthetic: false
    };
    expect(compareTreeNodes(a, b)).toBeLessThan(0);
    expect(compareTreeNodes(b, a)).toBeGreaterThan(0);
  });

  it("sorts numeric names naturally", () => {
    const a10 = {
      children: new Map(),
      kind: "ENTRY_KIND_FILE" as const,
      name: "file-10",
      path: "/file-10",
      synthetic: false
    };
    const a2 = {
      children: new Map(),
      kind: "ENTRY_KIND_FILE" as const,
      name: "file-2",
      path: "/file-2",
      synthetic: false
    };
    expect(compareTreeNodes(a2, a10)).toBeLessThan(0);
    expect(compareTreeNodes(a10, a2)).toBeGreaterThan(0);
  });
});

describe("compareRepositoryPaths", () => {
  it("compares paths alphabetically", () => {
    expect(compareRepositoryPaths("/a", "/b")).toBeLessThan(0);
    expect(compareRepositoryPaths("/b", "/a")).toBeGreaterThan(0);
    expect(compareRepositoryPaths("/a", "/a")).toBe(0);
  });

  it("compares paths numerically", () => {
    expect(compareRepositoryPaths("/dir-2", "/dir-10")).toBeLessThan(0);
    expect(compareRepositoryPaths("/dir-10", "/dir-2")).toBeGreaterThan(0);
  });
});

describe("pathSearchValue", () => {
  it("returns normalized string for valid input", () => {
    expect(pathSearchValue("/foo/bar")).toBe("/foo/bar");
  });

  it("returns empty string for non-string input", () => {
    expect(pathSearchValue(123)).toBe("");
    expect(pathSearchValue(null)).toBe("");
    expect(pathSearchValue(undefined)).toBe("");
  });

  it("returns empty string for whitespace-only string", () => {
    expect(pathSearchValue("   ")).toBe("");
  });
});

describe("buildSourceTree", () => {
  it("creates empty root for no included paths", () => {
    const directoryResults: DirectoryLoadResult[] = [];
    const tree = buildSourceTree([], directoryResults);

    expect(tree.path).toBe("");
    expect(tree.name).toBe("slice root");
    expect(tree.kind).toBe("ENTRY_KIND_DIRECTORY");
    expect(tree.children.size).toBe(0);
  });

  it("creates tree with included paths", () => {
    const tree = buildSourceTree(["/foo/bar"], []);

    expect(tree.children.size).toBe(1);
    const foo = tree.children.get("/foo");
    expect(foo).toBeDefined();
    expect(foo?.name).toBe("foo");
    expect(foo?.synthetic).toBe(true);
  });

  it("populates tree from directory results", () => {
    const directoryResults: DirectoryLoadResult[] = [
      {
        path: "/",
        data: [
          { kind: "ENTRY_KIND_FILE", name: "README.md", path: "/README.md" },
          { kind: "ENTRY_KIND_DIRECTORY", name: "src", path: "/src" }
        ],
        error: null,
        isLoading: false
      }
    ];

    const tree = buildSourceTree(["/"], directoryResults);

    expect(tree.children.size).toBe(2);
    const readme = tree.children.get("/README.md");
    const src = tree.children.get("/src");

    expect(readme?.kind).toBe("ENTRY_KIND_FILE");
    expect(readme?.synthetic).toBe(false);
    expect(src?.kind).toBe("ENTRY_KIND_DIRECTORY");
    expect(src?.synthetic).toBe(false);
  });
});

describe("flattenTreeRows", () => {
  it("returns empty array when root not expanded", () => {
    const tree = {
      children: new Map(),
      kind: "ENTRY_KIND_DIRECTORY" as const,
      name: "root",
      path: "",
      synthetic: false
    };
    const expandedPaths = new Set<string>();
    const rows = flattenTreeRows(tree, expandedPaths);

    expect(rows).toEqual([]);
  });

  it("flattens tree with expanded paths", () => {
    const tree: import("./sourceTree").SourceTreeNode = {
      children: new Map([
        [
          "/dir1",
          {
            children: new Map([
              [
                "/dir1/file2.ts",
                {
                  children: new Map(),
                  kind: "ENTRY_KIND_FILE",
                  name: "file2.ts",
                  path: "/dir1/file2.ts",
                  synthetic: false
                }
              ]
            ]),
            kind: "ENTRY_KIND_DIRECTORY",
            name: "dir1",
            path: "/dir1",
            synthetic: false
          }
        ],
        [
          "/file1.ts",
          {
            children: new Map(),
            kind: "ENTRY_KIND_FILE",
            name: "file1.ts",
            path: "/file1.ts",
            synthetic: false
          }
        ]
      ]),
      kind: "ENTRY_KIND_DIRECTORY",
      name: "root",
      path: "",
      synthetic: false
    };

    const expandedPaths = new Set(["", "/dir1"]);
    const rows = flattenTreeRows(tree, expandedPaths);

    expect(rows).toHaveLength(3);
    expect(rows[0].depth).toBe(0);
    expect(rows[0].node.name).toBe("dir1");
    expect(rows[1].depth).toBe(1);
    expect(rows[1].node.name).toBe("file2.ts");
    expect(rows[2].depth).toBe(0);
    expect(rows[2].node.name).toBe("file1.ts");
  });

  it("skips collapsed directories", () => {
    const tree: import("./sourceTree").SourceTreeNode = {
      children: new Map([
        [
          "/dir1",
          {
            children: new Map([
              [
                "/dir1/file2.ts",
                {
                  children: new Map(),
                  kind: "ENTRY_KIND_FILE",
                  name: "file2.ts",
                  path: "/dir1/file2.ts",
                  synthetic: false
                }
              ]
            ]),
            kind: "ENTRY_KIND_DIRECTORY",
            name: "dir1",
            path: "/dir1",
            synthetic: false
          }
        ],
        [
          "/file1.ts",
          {
            children: new Map(),
            kind: "ENTRY_KIND_FILE",
            name: "file1.ts",
            path: "/file1.ts",
            synthetic: false
          }
        ]
      ]),
      kind: "ENTRY_KIND_DIRECTORY",
      name: "root",
      path: "",
      synthetic: false
    };

    const expandedPaths = new Set([""]);
    const rows = flattenTreeRows(tree, expandedPaths);

    expect(rows).toHaveLength(2);
    expect(rows[0].node.name).toBe("dir1");
    expect(rows[1].node.name).toBe("file1.ts");
  });
});

describe("initialTreeExpansion", () => {
  it("expands root path", () => {
    const expanded = initialTreeExpansion("", [], false);
    expect(expanded).toContain("");
  });

  it("expands selected directory path", () => {
    const expanded = initialTreeExpansion("/foo/bar/baz", [], true);
    expect(expanded).toContain("/foo");
    expect(expanded).toContain("/foo/bar");
    expect(expanded).toContain("/foo/bar/baz");
  });

  it("expands parent of selected file path", () => {
    const expanded = initialTreeExpansion("/foo/bar/file.ts", [], false);
    expect(expanded).toContain("/foo");
    expect(expanded).toContain("/foo/bar");
    expect(expanded).not.toContain("/foo/bar/file.ts");
  });

  it("expands included paths", () => {
    const expanded = initialTreeExpansion("", ["/foo/bar", "/baz"], false);
    expect(expanded).toContain("/foo");
    expect(expanded).toContain("/foo/bar");
    expect(expanded).toContain("/baz");
  });
});