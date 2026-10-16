import { describe, expect, it } from "vitest";

import type { TreeEntry } from "../../api/types";
import { findReadmeEntry } from "./readme";

const file = (name: string): TreeEntry => ({ kind: "ENTRY_KIND_FILE", name, path: `/acme/app/${name}` });
const dir = (name: string): TreeEntry => ({ kind: "ENTRY_KIND_DIRECTORY", name, path: `/acme/app/${name}` });

describe("findReadmeEntry", () => {
  it("finds a README in any casing", () => {
    for (const name of ["README.md", "readme.md", "Readme.MD", "README.markdown"]) {
      expect(findReadmeEntry([file("main.go"), file(name)])?.name).toBe(name);
    }
  });

  it("prefers README.md, then other casings, then README.markdown", () => {
    expect(findReadmeEntry([file("readme.md"), file("README.md")])?.name).toBe("README.md");
    expect(findReadmeEntry([file("README.markdown"), file("readme.md")])?.name).toBe("readme.md");
    expect(findReadmeEntry([file("readme.markdown"), file("README.markdown")])?.name).toBe("README.markdown");
  });

  it("ignores directories and other files", () => {
    expect(findReadmeEntry([dir("README.md"), file("README.txt"), file("README"), file("docs-readme.md")])).toBeUndefined();
    expect(findReadmeEntry([])).toBeUndefined();
  });

  it("takes the name from the path when an entry has none", () => {
    expect(findReadmeEntry([{ kind: "ENTRY_KIND_FILE", path: "/acme/app/README.md" }])?.path).toBe("/acme/app/README.md");
  });
});
