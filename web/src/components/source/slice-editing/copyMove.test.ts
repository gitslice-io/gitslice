import { describe, expect, it, vi } from "vitest";

import type { TreeEntry } from "../../../api/types";
import type { ApiClient } from "../../../api/useApi";

import {
  COPY_ENTRY_LIMIT,
  CopyTooLargeError,
  destinationProblem,
  intoFolder,
  planCopy,
  planMove,
  readCopySource,
  resolveDestination,
  skippedEntries
} from "./copyMove";

const file = { path: "/src/a.go", isDirectory: false };
const folder = { path: "/src/pkg", isDirectory: true };

describe("resolveDestination", () => {
  it("resolves relative paths against the source's folder", () => {
    expect(resolveDestination("b.go", file)).toEqual({
      path: "/src/b.go",
      into: false
    });
    expect(resolveDestination("../docs/b.go", file)).toEqual({
      path: "/docs/b.go",
      into: false
    });
    expect(resolveDestination("./sub/b.go", file)).toEqual({
      path: "/src/sub/b.go",
      into: false
    });
  });

  it("keeps absolute paths and marks a trailing slash as into", () => {
    expect(resolveDestination("/docs/", file)).toEqual({
      path: "/docs",
      into: true
    });
    expect(resolveDestination("  /docs/x.go ", file)).toEqual({
      path: "/docs/x.go",
      into: false
    });
  });

  it("rejects empty input, escapes above the root and the root itself", () => {
    expect(resolveDestination("  ", file)).toHaveProperty("error");
    expect(resolveDestination("../../x", file)).toHaveProperty("error");
    expect(resolveDestination("/", file)).toHaveProperty("error");
    expect(resolveDestination("/a/\u0001b", file)).toHaveProperty("error");
  });
});

describe("destinationProblem", () => {
  const canModify = (path: string) => path.startsWith("/src/");

  it("accepts a writable, different destination", () => {
    expect(
      destinationProblem({ canModify, destination: "/src/b.go", mode: "copy", source: file })
    ).toBe("");
  });

  it("refuses the source itself, a folder inside itself and unwritable paths", () => {
    expect(
      destinationProblem({ canModify, destination: "/src/a.go", mode: "move", source: file })
    ).not.toBe("");
    expect(
      destinationProblem({ canModify, destination: "/src/pkg/sub", mode: "move", source: folder })
    ).toMatch(/inside itself/);
    expect(
      destinationProblem({ canModify, destination: "/other/a.go", mode: "copy", source: file })
    ).toMatch(/outside/);
  });

  it("allows a sibling whose name only starts like the folder", () => {
    expect(
      destinationProblem({ canModify, destination: "/src/pkg2", mode: "move", source: folder })
    ).toBe("");
  });
});

describe("plans", () => {
  it("moves with one rename", () => {
    expect(planMove(folder, "/src/new")).toEqual([
      { kind: "rename", oldPath: "/src/pkg", path: "/src/new" }
    ]);
    expect(intoFolder("/docs", file)).toBe("/docs/a.go");
  });

  it("copies a file by reference, keeping its mode", () => {
    const entry: TreeEntry = {
      path: "/src/a.go",
      kind: "ENTRY_KIND_FILE",
      blobId: "blob1",
      contentHash: "hash1",
      mode: 0o100755
    };
    expect(planCopy(file, "/docs/a.go", [entry])).toEqual([
      {
        kind: "write",
        path: "/docs/a.go",
        isNew: true,
        blobId: "blob1",
        contentHash: "hash1",
        mode: 0o100755
      }
    ]);
  });

  it("copies a folder as a folder per directory and a write per file", () => {
    const entries: TreeEntry[] = [
      { path: "/src/pkg/empty", kind: "ENTRY_KIND_DIRECTORY" },
      { path: "/src/pkg/x.go", kind: "ENTRY_KIND_FILE", blobId: "b", contentHash: "h" },
      { path: "/src/pkg/sub/y.go", kind: "ENTRY_KIND_FILE", blobId: "c", contentHash: "i" }
    ];
    expect(planCopy(folder, "/lib/pkg", entries)).toEqual([
      { kind: "mkdir", path: "/lib/pkg" },
      { kind: "mkdir", path: "/lib/pkg/empty" },
      { kind: "write", path: "/lib/pkg/x.go", isNew: true, blobId: "b", contentHash: "h" },
      { kind: "write", path: "/lib/pkg/sub/y.go", isNew: true, blobId: "c", contentHash: "i" }
    ]);
  });

  it("counts symlinks as skipped", () => {
    expect(
      skippedEntries([
        { kind: "ENTRY_KIND_FILE" },
        { kind: "ENTRY_KIND_SYMLINK" }
      ])
    ).toBe(1);
  });
});

describe("readCopySource", () => {
  function fakeApi(listDirectory: ApiClient["listDirectory"]) {
    return { listDirectory, resolvePath: vi.fn() } as unknown as ApiClient;
  }

  it("lists a folder recursively in one call", async () => {
    const listDirectory = vi.fn().mockResolvedValue({
      entries: [{ path: "/src/pkg/x.go", kind: "ENTRY_KIND_FILE" }],
      recursive: true
    });
    const entries = await readCopySource(fakeApi(listDirectory), {
      commitId: "c1",
      source: folder
    });
    expect(entries).toHaveLength(1);
    expect(listDirectory).toHaveBeenCalledTimes(1);
    expect(listDirectory.mock.calls[0][0]).toMatchObject({
      path: "/src/pkg",
      recursive: true
    });
  });

  it("walks folder by folder when the server lists one level", async () => {
    const listDirectory = vi.fn(async (request: { path?: string }) => {
      if (request.path === "/src/pkg") {
        return {
          entries: [
            { path: "/src/pkg/sub", kind: "ENTRY_KIND_DIRECTORY" },
            { path: "/src/pkg/x.go", kind: "ENTRY_KIND_FILE" }
          ]
        };
      }
      return { entries: [{ path: "/src/pkg/sub/y.go", kind: "ENTRY_KIND_FILE" }] };
    });
    const entries = await readCopySource(fakeApi(listDirectory as never), {
      commitId: "c1",
      source: folder
    });
    expect(entries.map((entry) => entry.path)).toEqual([
      "/src/pkg/sub",
      "/src/pkg/x.go",
      "/src/pkg/sub/y.go"
    ]);
  });

  it("refuses a folder above the item limit", async () => {
    const listDirectory = vi.fn().mockResolvedValue({
      entries: Array.from({ length: COPY_ENTRY_LIMIT + 1 }, (_, index) => ({
        path: `/src/pkg/f${index}`,
        kind: "ENTRY_KIND_FILE"
      })),
      recursive: true
    });
    await expect(
      readCopySource(fakeApi(listDirectory), { commitId: "c1", source: folder })
    ).rejects.toBeInstanceOf(CopyTooLargeError);
  });
});
