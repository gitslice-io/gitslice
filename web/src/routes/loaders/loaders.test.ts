import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import type { Changeset, Slice } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { checkRunsQuery } from "../../queries/checks";
import { sliceFileQueryKey, sliceRenderedFileQueryKey } from "../../queries/slices";
import { stripUndefined } from "../root";
import { changesetSocial, prefetchChangesetDetailSSR } from "./changesetDetailLoader";
import { prefetchSliceDetailSSR, sliceSocial } from "./sliceDetailLoader";

describe("stripUndefined", () => {
  it("preserves primitives and null", () => {
    expect(stripUndefined(123)).toBe(123);
    expect(stripUndefined("hello")).toBe("hello");
    expect(stripUndefined(true)).toBe(true);
    expect(stripUndefined(null)).toBe(null);
  });

  it("removes undefined keys from objects", () => {
    const input = { a: 1, b: undefined, c: "test", d: null };
    expect(stripUndefined(input)).toEqual({ a: 1, c: "test", d: null });
  });

  it("replaces undefined array items with null", () => {
    const input = [1, undefined, 3];
    expect(stripUndefined(input)).toEqual([1, null, 3]);
  });

  it("converts Date instances to ISO string", () => {
    const date = new Date("2026-01-01T00:00:00.000Z");
    expect(stripUndefined(date)).toBe("2026-01-01T00:00:00.000Z");
  });

  it("handles nested structures recursively", () => {
    const nested = {
      level1: {
        val: undefined,
        arr: [{ keep: "yes", drop: undefined }]
      }
    };
    expect(stripUndefined(nested)).toEqual({
      level1: {
        arr: [{ keep: "yes" }]
      }
    });
  });
});

describe("sliceSocial", () => {
  it("formats metadata for social previews", () => {
    const slice: Slice = {
      ref: { account: "alice", slice: "frontend" },
      description: "Frontend slice"
    };
    const res = sliceSocial(
      slice,
      { account: "alice", slice: "frontend" },
      "/src/App.tsx",
      "https://example.com"
    );
    expect(res).toEqual({
      account: "alice",
      slice: "frontend",
      description: "Frontend slice",
      path: "/src/App.tsx",
      origin: "https://example.com"
    });
  });
});

describe("changesetSocial", () => {
  it("formats metadata for changeset social previews", () => {
    const cs: Changeset = {
      id: "cs_1234567890abcdef1234567890abcdef",
      title: "Fix bug",
      description: "Fixed it",
      status: "CHANGESET_STATUS_SUBMITTED",
      author: "bob",
      authoringSlice: { account: "alice", slice: "frontend" },
      affectedPaths: ["foo.ts", "bar.ts"]
    };
    const res = changesetSocial(
      cs,
      "cs_1234567890abcdef1234567890abcdef",
      "https://example.com"
    );
    expect(res).toEqual({
      handle: "1234567890",
      title: "Fix bug",
      description: "Fixed it",
      status: "CHANGESET_STATUS_SUBMITTED",
      author: "bob",
      account: "alice",
      slice: "frontend",
      fileCount: 2,
      origin: "https://example.com"
    });
  });
});

describe("prefetchSliceDetailSSR", () => {
  it("prefetches slice, global ref, directory listing, and guessed README", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const mockSlice: Slice = {
      ref: { account: "acct", slice: "my-slice" },
      description: "test",
      definition: { includedPaths: ["src"] }
    };
    const mockApi = {
      resolveSlice: vi.fn().mockResolvedValue(mockSlice),
      getRef: vi.fn().mockResolvedValue({ commitId: "c1" }),
      listDirectory: vi.fn().mockImplementation(async ({ path }: { path: string }) => {
        if (path === "") {
          return {
            entries: [{ name: "src", path: "src", kind: "ENTRY_KIND_DIRECTORY" }],
            nextPageToken: ""
          };
        }
        return {
          entries: [
            { name: "README.md", path: "src/README.md", kind: "ENTRY_KIND_FILE", size: "120" }
          ],
          nextPageToken: ""
        };
      }),
      renderFile: vi.fn().mockResolvedValue({ html: "<h1>Hi</h1>" }),
      getAuthStatus: vi.fn().mockResolvedValue({ authenticated: true })
    } as unknown as ApiClient;

    const social = await prefetchSliceDetailSSR({
      queryClient,
      api: mockApi,
      signedIn: true,
      account: "acct",
      slice: "my-slice",
      selectedPath: "",
      origin: "https://example.com",
      lookup: async (fn) => fn(),
      budgetMs: 500
    });

    expect(social?.slice).toBe("my-slice");
    expect(mockApi.resolveSlice).toHaveBeenCalledWith({
      ref: { account: "acct", slice: "my-slice" }
    });
    expect(mockApi.getRef).toHaveBeenCalled();
    expect(mockApi.getAuthStatus).toHaveBeenCalled();
    expect(
      queryClient.getQueryData(
        sliceRenderedFileQueryKey({ account: "acct", slice: "my-slice" }, "c1", "/src/README.md")
      )
    ).toEqual({ html: "<h1>Hi</h1>" });
  });

  it("drops guessed README when actual directory has no README and resolves selected file path", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const ref = { account: "acct", slice: "my-slice" };
    const mockSlice: Slice = {
      ref,
      definition: { includedPaths: ["src"] }
    };
    const mockApi = {
      resolveSlice: vi.fn().mockResolvedValue(mockSlice),
      getRef: vi.fn().mockResolvedValue({ commitId: "c1" }),
      listDirectory: vi.fn().mockResolvedValue({ entries: [], nextPageToken: "" }),
      resolvePath: vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_FILE" } }),
      readFile: vi.fn().mockResolvedValue({ content: new Uint8Array([65]) }),
      renderFile: vi.fn().mockResolvedValue({ html: "<p>wrong</p>" })
    } as unknown as ApiClient;

    await prefetchSliceDetailSSR({
      queryClient,
      api: mockApi,
      signedIn: false,
      account: "acct",
      slice: "my-slice",
      selectedPath: "",
      origin: "https://example.com",
      lookup: async (fn) => fn(),
      budgetMs: 500
    });

    expect(
      queryClient.getQueryData(sliceRenderedFileQueryKey(ref, "c1", "src/README.md"))
    ).toBeUndefined();

    await prefetchSliceDetailSSR({
      queryClient,
      api: mockApi,
      signedIn: false,
      account: "acct",
      slice: "my-slice",
      selectedPath: "src/app.ts",
      origin: "https://example.com",
      lookup: async (fn) => fn(),
      budgetMs: 500
    });

    expect(queryClient.getQueryData(sliceFileQueryKey(ref, "c1", "src/app.ts"))).toEqual({
      content: new Uint8Array([65])
    });
  });

  it("renders a pinned commit (?at=) instead of the latest", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const ref = { account: "acct", slice: "my-slice" };
    const mockApi = {
      resolveSlice: vi.fn().mockResolvedValue({ ref, definition: { includedPaths: ["src"] } }),
      getRef: vi.fn().mockResolvedValue({ commitId: "latest" }),
      listDirectory: vi.fn().mockResolvedValue({ entries: [], nextPageToken: "" }),
      resolvePath: vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_FILE" } }),
      readFile: vi.fn().mockResolvedValue({ content: new Uint8Array([66]) }),
      renderFile: vi.fn().mockResolvedValue({ html: "" })
    } as unknown as ApiClient;

    await prefetchSliceDetailSSR({
      queryClient,
      api: mockApi,
      signedIn: false,
      account: "acct",
      slice: "my-slice",
      selectedPath: "src/app.ts",
      at: "tagged",
      origin: "https://example.com",
      lookup: async (fn) => fn(),
      budgetMs: 500
    });

    expect(mockApi.getRef).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(sliceFileQueryKey(ref, "tagged", "src/app.ts"))).toEqual({
      content: new Uint8Array([66])
    });
  });
});

describe("prefetchChangesetDetailSSR", () => {
  it("prefetches changeset, diff, dependents, and drops non-terminal check runs from SSR cache", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const mockCs: Changeset = {
      id: "cs_abc",
      title: "Feature",
      patchsets: [{ id: "ps_1" }],
      currentPatchsetId: "ps_1",
      authoringSlice: { account: "acct", slice: "sl" }
    };
    const mockApi = {
      getChangeset: vi.fn().mockResolvedValue(mockCs),
      diffChangeset: vi.fn().mockResolvedValue({ diff: "diff --git a/x b/x" }),
      listAutomationRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listCheckRuns: vi
        .fn()
        .mockResolvedValue({ runs: [{ id: "cr_1", status: "CHECK_RUN_STATUS_RUNNING" }] }),
      resolveSlice: vi.fn().mockResolvedValue({ ref: { account: "acct", slice: "sl" } }),
      listChangesets: vi.fn().mockResolvedValue({
        changesets: [{ id: "cs_child", title: "Child", parentChangesetId: "cs_abc" }]
      })
    } as unknown as ApiClient;

    const social = await prefetchChangesetDetailSSR({
      queryClient,
      api: mockApi,
      signedIn: true,
      changesetId: "cs_abc",
      origin: "https://example.com",
      lookup: async (fn) => fn()
    });

    expect(social.title).toBe("Feature");
    expect(mockApi.getChangeset).toHaveBeenCalledWith({ changesetId: "cs_abc" });
    expect(mockApi.diffChangeset).toHaveBeenCalledWith({
      changesetId: "cs_abc",
      fromPatchset: undefined,
      toPatchset: "ps_1"
    });
    // Non-terminal check runs are removed from SSR cache so browser polling handles moving timestamps.
    expect(queryClient.getQueryData(checkRunsQuery(mockApi, "cs_abc", "ps_1").queryKey)).toBeUndefined();
  });
});
