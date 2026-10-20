import { describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../../api/useApi";

import {
  accountOfPath,
  destinationProblem,
  findDestinationSlice,
  intoFolder,
  resolveDestination
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

describe("intoFolder", () => {
  it("keeps the item's name inside the folder", () => {
    expect(intoFolder("/docs", file)).toBe("/docs/a.go");
  });
});

describe("findDestinationSlice", () => {
  const slice = (name: string, included: string[], archived = false) => ({
    archived,
    ref: { account: "acme", slice: name },
    definition: { includedPaths: included }
  });

  function apiWith(pages: unknown[]) {
    const listSlices = vi.fn();
    pages.forEach((page) => listSlices.mockResolvedValueOnce(page));
    return { api: { listSlices } as unknown as ApiClient, listSlices };
  }

  it("takes the account from the path's first folder", () => {
    expect(accountOfPath("/acme/docs/x")).toBe("acme");
    expect(accountOfPath("/")).toBe("");
  });

  it("picks the most specific slice across pages, skipping archived ones", async () => {
    const { api, listSlices } = apiWith([
      { slices: [slice("acme", ["/acme"])], nextCursor: "p2" },
      { slices: [slice("docs", ["/acme/docs"]), slice("old", ["/acme/docs/x"], true)] }
    ]);
    const found = await findDestinationSlice(api, "/acme/docs/x/y.md");
    expect(found?.ref?.slice).toBe("docs");
    expect(listSlices).toHaveBeenCalledTimes(2);
  });

  it("finds nothing for a slice's own root or an uncovered path", async () => {
    const { api } = apiWith([
      { slices: [slice("docs", ["/acme/docs"])] },
      { slices: [slice("docs", ["/acme/docs"])] }
    ]);
    expect(await findDestinationSlice(api, "/acme/docs")).toBeUndefined();
    expect(await findDestinationSlice(api, "/acme/other/x")).toBeUndefined();
  });
});
