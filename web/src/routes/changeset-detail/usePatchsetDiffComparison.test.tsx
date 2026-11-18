import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Changeset } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import {
  EAGER_FILE_DIFF_COUNT,
  FULL_DIFF_PATH_LIMIT,
  usePatchsetDiffComparison
} from "./usePatchsetDiffComparison";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } }
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
  };
}

describe("usePatchsetDiffComparison", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("fetches a single whole-changeset diff when changed paths are <= FULL_DIFF_PATH_LIMIT", async () => {
    const api = {
      diffChangeset: vi.fn().mockResolvedValue({ diff: "diff --git a/a.ts b/a.ts\n" })
    } as unknown as ApiClient;
    const changeset: Changeset = {
      id: "cs_1",
      currentPatchsetId: "ps_1",
      patchsets: [
        {
          id: "ps_1",
          number: "1",
          fileEdits: [
            { path: "a.ts", op: "modify" },
            { path: "b.ts", op: "modify" }
          ]
        }
      ]
    };

    const { result } = renderHook(
      () =>
        usePatchsetDiffComparison({
          api,
          changeset,
          canonicalChangesetId: "cs_1"
        }),
      { wrapper: createWrapper() }
    );

    expect(result.current.usesLazyFileDiffs).toBe(false);
    expect(result.current.fileStates).toBeUndefined();
    await waitFor(() => expect(result.current.diffQuery.isSuccess).toBe(true));
    expect(api.diffChangeset).toHaveBeenCalledTimes(1);
    expect(api.diffChangeset).toHaveBeenCalledWith({
      changesetId: "cs_1",
      fromPatchset: undefined,
      toPatchset: "ps_1"
    });
  });

  it("switches to per-file lazy diff fan-out above FULL_DIFF_PATH_LIMIT and loads demanded paths", async () => {
    const paths = Array.from(
      { length: FULL_DIFF_PATH_LIMIT + 3 },
      (_, idx) => `src/file_${String(idx).padStart(2, "0")}.ts`
    );
    const api = {
      diffChangeset: vi.fn().mockImplementation(async ({ paths: reqPaths }: { paths?: string[] }) => ({
        diff: `diff --git a/${reqPaths?.[0]} b/${reqPaths?.[0]}\n--- a/${reqPaths?.[0]}\n+++ b/${reqPaths?.[0]}\n@@ -1 +1 @@\n-old\n+new\n`
      }))
    } as unknown as ApiClient;
    const changeset: Changeset = {
      id: "cs_big",
      currentPatchsetId: "ps_1",
      patchsets: [
        {
          id: "ps_1",
          number: "1",
          fileEdits: paths.map((path) => ({ path, op: "modify" }))
        }
      ]
    };

    const { result } = renderHook(
      () =>
        usePatchsetDiffComparison({
          api,
          changeset,
          canonicalChangesetId: "cs_big"
        }),
      { wrapper: createWrapper() }
    );

    expect(result.current.usesLazyFileDiffs).toBe(true);
    await waitFor(() =>
      expect(
        result.current.fileStates
          ?.slice(0, EAGER_FILE_DIFF_COUNT)
          .every((state) => state.status === "loaded")
      ).toBe(true)
    );
    expect(api.diffChangeset).toHaveBeenCalledTimes(EAGER_FILE_DIFF_COUNT);
    const tailPath = paths[paths.length - 1];
    expect(
      result.current.fileStates?.find((s) => s.path === tailPath)?.status
    ).toBe("pending");

    act(() => {
      result.current.requestFileDiff(tailPath);
    });

    await waitFor(() =>
      expect(
        result.current.fileStates?.find((s) => s.path === tailPath)?.status
      ).toBe("loaded")
    );
    expect(api.diffChangeset).toHaveBeenCalledTimes(EAGER_FILE_DIFF_COUNT + 1);
  });
});
