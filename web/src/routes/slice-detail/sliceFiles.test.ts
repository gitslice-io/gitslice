import { describe, expect, it, vi } from "vitest";

import { displayPath, listSliceFiles, sharedRoot } from "./sliceFiles";

const slice = { account: "acme", slice: "app" };
const file = (path: string) => ({ kind: "ENTRY_KIND_FILE" as const, path });
const dir = (path: string) => ({ kind: "ENTRY_KIND_DIRECTORY" as const, path });

describe("listSliceFiles", () => {
  it("lists every included path recursively, page after page, keeping only files", async () => {
    const listDirectory = vi.fn(async (request: { path?: string; cursor?: string }) => {
      if (request.path === "/acme/app") {
        return request.cursor
          ? { recursive: true, entries: [file("/acme/app/z.go")] }
          : { recursive: true, entries: [dir("/acme/app/src"), file("/acme/app/src/a.go")], nextCursor: "c1" };
      }
      return { recursive: true, entries: [file("/acme/shared/s.go"), file("/acme/app/src/a.go")] };
    });
    const result = await listSliceFiles({ listDirectory } as never, slice, "commit1", ["/acme/app", "/acme/shared"]);

    expect(result).toEqual({ paths: ["/acme/app/src/a.go", "/acme/app/z.go", "/acme/shared/s.go"], partial: false });
    expect(listDirectory).toHaveBeenCalledTimes(3);
    expect(listDirectory.mock.calls[0][0]).toEqual({
      commitId: "commit1",
      cursor: undefined,
      pageSize: 10000,
      path: "/acme/app",
      recursive: true,
      slice
    });
    expect(listDirectory.mock.calls[1][0]).toEqual(expect.objectContaining({ cursor: "c1", path: "/acme/app" }));
  });

  it("says so when the server could only list one level", async () => {
    const listDirectory = vi.fn().mockResolvedValue({ entries: [file("/acme/app/top.go")] });
    expect(await listSliceFiles({ listDirectory } as never, slice, "c", ["/acme/app"])).toEqual({ paths: [], partial: true });
  });

  it("handles an included path that is a file, and a slice with none", async () => {
    const listDirectory = vi.fn().mockResolvedValue({ recursive: true, entries: [file("/acme/app/one.txt")] });
    expect((await listSliceFiles({ listDirectory } as never, slice, "c", ["/acme/app/one.txt"])).paths).toEqual(["/acme/app/one.txt"]);
    expect(await listSliceFiles({ listDirectory } as never, slice, "c", [])).toEqual({ paths: [], partial: false });
  });

  it("lets an error through, for the finder to show", async () => {
    const listDirectory = vi.fn().mockRejectedValue(new Error("boom"));
    await expect(listSliceFiles({ listDirectory } as never, slice, "c", ["/acme/app"])).rejects.toThrow("boom");
  });
});

describe("paths as the finder shows them", () => {
  it("drops the folder a one-path slice sits in, and keeps whole paths otherwise", () => {
    expect(sharedRoot(["/gitslice/gitslice/"])).toBe("/gitslice/gitslice");
    expect(sharedRoot(["/a", "/b"])).toBe("");
    expect(sharedRoot([])).toBe("");
    expect(displayPath("/gitslice/gitslice/web/x.ts", "/gitslice/gitslice")).toBe("web/x.ts");
    expect(displayPath("/gitslice/gitslice", "/gitslice/gitslice")).toBe("gitslice/gitslice");
    expect(displayPath("/b/y.ts", "")).toBe("b/y.ts");
    // A file whose name starts like the root is not inside it.
    expect(displayPath("/gitslice/gitslice-extra/x.ts", "/gitslice/gitslice")).toBe("gitslice/gitslice-extra/x.ts");
  });
});
