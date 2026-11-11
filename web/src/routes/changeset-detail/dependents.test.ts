import { describe, expect, it, vi } from "vitest";

import { dependentChangesetsQuery } from "./dependents";

describe("dependentChangesetsQuery", () => {
  const slice = { account: "acme", slice: "shop" };

  it("keeps only the ids and titles of the changesets stacked on this one", async () => {
    const listChangesets = vi.fn(async () => ({
      changesets: [
        { id: "cs_child", title: "Child", parentChangesetId: "cs_root", affectedPaths: ["/a", "/b"], description: "long" },
        { id: "cs_other", title: "Other", parentChangesetId: "cs_else" },
        { id: "cs_grandchild", title: "Grandchild", parentChangesetId: "cs_child" },
        { id: "cs_untitled", parentChangesetId: "cs_root" },
        { title: "No id", parentChangesetId: "cs_root" }
      ]
    }));
    const query = dependentChangesetsQuery({ listChangesets } as never, slice, "cs_root");

    await expect(query.queryFn()).resolves.toEqual([
      { id: "cs_child", title: "Child" },
      { id: "cs_untitled", title: "" }
    ]);
    expect(listChangesets).toHaveBeenCalledWith({ authoringSlice: slice, limit: 200, summary: true });
  });

  it("is cached per changeset", () => {
    const api = { listChangesets: vi.fn() } as never;
    expect(dependentChangesetsQuery(api, slice, "cs_a").queryKey).not.toEqual(
      dependentChangesetsQuery(api, slice, "cs_b").queryKey
    );
  });
});
