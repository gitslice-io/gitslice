import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import {
  accountSlicesQuery,
  dependentCandidatesQuery,
  sliceChangesetsQuery
} from "./queries";
import type { ApiClient } from "./useApi";

function fetchQuery(options: { queryKey: readonly unknown[] }) {
  return new QueryClient().fetchQuery(options as never);
}

describe("accountSlicesQuery", () => {
  it("follows pagination and keys the result by account", async () => {
    const listSlices = vi
      .fn()
      .mockResolvedValueOnce({ slices: [{ id: "a" }], nextCursor: "next" })
      .mockResolvedValueOnce({ slices: [{ id: "b" }] });
    const api = { listSlices } as unknown as ApiClient;
    const options = accountSlicesQuery(api, "nic");

    expect(options.queryKey).toEqual(["slices", "nic"]);
    await expect(fetchQuery(options)).resolves.toEqual([
      { id: "a" },
      { id: "b" }
    ]);
    expect(listSlices).toHaveBeenNthCalledWith(2, {
      account: "nic",
      cursor: "next",
      pageSize: 100
    });
  });
});

describe("sliceChangesetsQuery", () => {
  it("drops patchsets but keeps the fields the list renders", async () => {
    const listChangesets = vi.fn().mockResolvedValue({
      changesets: [
        {
          id: "cs_1",
          title: "Fix",
          affectedPaths: ["/nic/a.go"],
          currentPatchsetId: "ps_2",
          patchsets: [{ id: "ps_2", fileEdits: [{ path: "/nic/a.go" }] }]
        }
      ]
    });
    const api = { listChangesets } as unknown as ApiClient;
    const options = sliceChangesetsQuery(api, "nic", "home");

    expect(options.queryKey).toEqual(["changesets", "nic", "home"]);
    const result = await fetchQuery(options);
    expect(result).toEqual({
      changesets: [
        {
          id: "cs_1",
          title: "Fix",
          affectedPaths: ["/nic/a.go"],
          currentPatchsetId: "ps_2"
        }
      ]
    });
    expect(listChangesets).toHaveBeenCalledWith({
      authoringSlice: { account: "nic", slice: "home" }
    });
  });
});

describe("dependentCandidatesQuery", () => {
  it("keeps only what the dependents list needs", async () => {
    const listChangesets = vi.fn().mockResolvedValue({
      changesets: [
        {
          id: "cs_2",
          title: "Child",
          parentChangesetId: "cs_1",
          status: "draft",
          patchsets: [{ id: "ps_9" }]
        }
      ]
    });
    const api = { listChangesets } as unknown as ApiClient;
    const slice = { account: "nic", slice: "home" };
    const options = dependentCandidatesQuery(api, slice);

    expect(options.queryKey).toEqual(["changesetsBySlice", "nic", "home"]);
    await expect(fetchQuery(options)).resolves.toEqual({
      changesets: [{ id: "cs_2", title: "Child", parentChangesetId: "cs_1" }]
    });
    expect(listChangesets).toHaveBeenCalledWith({
      authoringSlice: slice,
      limit: 200
    });
  });
});
