import { describe, expect, it, vi } from "vitest";

import type { TreeEntry } from "../../api/types";
import { nestedLabel } from "./FolderWithReadme";
import { NESTED_README_MAX_DEPTH, findNestedReadme } from "./nestedReadme";

const file = (path: string): TreeEntry => ({ kind: "ENTRY_KIND_FILE", name: path.split("/").pop(), path });
const dir = (path: string): TreeEntry => ({ kind: "ENTRY_KIND_DIRECTORY", name: path.split("/").pop(), path });

function lister(tree: Record<string, TreeEntry[]>) {
  return vi.fn(async (path: string) => tree[path] ?? []);
}

describe("findNestedReadme", () => {
  it("prefers the folder's own README and lists nothing", async () => {
    const list = lister({});
    const readme = await findNestedReadme([dir("/sunny/games"), file("/sunny/README.md")], list);
    expect(readme?.path).toBe("/sunny/README.md");
    expect(list).not.toHaveBeenCalled();
  });

  it("goes down through single folders to the first README", async () => {
    const list = lister({
      "/sunny/macnativegames": [dir("/sunny/macnativegames/src")],
      "/sunny/macnativegames/src": [file("/sunny/macnativegames/src/README.md"), file("/sunny/macnativegames/src/main.c")]
    });
    const readme = await findNestedReadme([dir("/sunny/macnativegames")], list);
    expect(readme?.path).toBe("/sunny/macnativegames/src/README.md");
    expect(list.mock.calls.map(([path]) => path)).toEqual(["/sunny/macnativegames", "/sunny/macnativegames/src"]);
  });

  it("stops where the tree branches or a folder holds a file", async () => {
    const branching = lister({ "/a": [dir("/a/b"), dir("/a/c")], "/a/b": [file("/a/b/README.md")] });
    expect(await findNestedReadme([dir("/a")], branching)).toBeUndefined();
    expect(branching).toHaveBeenCalledTimes(1);

    const withFile = lister({ "/a": [dir("/a/b"), file("/a/LICENSE")], "/a/b": [file("/a/b/README.md")] });
    expect(await findNestedReadme([dir("/a")], withFile)).toBeUndefined();
    expect(await findNestedReadme([file("/main.go")], withFile)).toBeUndefined();
    expect(await findNestedReadme([], withFile)).toBeUndefined();
  });

  it("gives up after a bounded number of levels", async () => {
    let path = "";
    const tree: Record<string, TreeEntry[]> = {};
    for (let i = 0; i < NESTED_README_MAX_DEPTH + 3; i++) {
      tree[`${path}/d${i}`] = [dir(`${path}/d${i}/d${i + 1}`)];
      path = `${path}/d${i}`;
    }
    tree[path] = [file(`${path}/README.md`)];
    const list = lister(tree);
    expect(await findNestedReadme([dir("/d0")], list)).toBeUndefined();
    expect(list).toHaveBeenCalledTimes(NESTED_README_MAX_DEPTH);
  });
});

describe("nestedLabel", () => {
  it("names a README below the folder by its path from it", () => {
    expect(nestedLabel("/sunny", file("/sunny/macnativegames/README.md"))).toBe("macnativegames/README.md");
    expect(nestedLabel("/", file("/sunny/games/README.md"))).toBe("sunny/games/README.md");
  });

  it("leaves the folder's own README to its name", () => {
    expect(nestedLabel("/sunny", file("/sunny/README.md"))).toBeUndefined();
    expect(nestedLabel("/", file("/README.md"))).toBeUndefined();
  });
});
