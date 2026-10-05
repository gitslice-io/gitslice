import { describe, expect, it } from "vitest";

import { sliceBreadcrumbItems } from "./sliceRoutes";

describe("sliceBreadcrumbItems", () => {
  it("is one crumb for a slice in the viewer's own account", () => {
    expect(sliceBreadcrumbItems({ account: "nic", slice: "notes" }, "nic")).toEqual([
      { label: "nic:notes", params: { account: "nic", slice: "notes" }, to: "/slices/$account/$slice" }
    ]);
  });

  it("ignores case when comparing accounts", () => {
    expect(sliceBreadcrumbItems({ account: "Nic", slice: "notes" }, "nic")).toHaveLength(1);
  });

  it("names the owner for someone else's slice", () => {
    const items = sliceBreadcrumbItems({ account: "gitslice", slice: "gitslice" }, "nic");

    expect(items.map((item) => item.label)).toEqual(["@gitslice", "gitslice"]);
    expect(items[0].title).toBe("Owned by gitslice");
    expect(items[0].to).toBeUndefined();
    expect(items[1].to).toBe("/slices/$account/$slice");
  });

  it("names the owner for a visitor who is not signed in", () => {
    expect(sliceBreadcrumbItems({ account: "gitslice", slice: "gitslice" }, "").map((item) => item.label)).toEqual([
      "@gitslice",
      "gitslice"
    ]);
  });

  it("has nothing for an incomplete reference", () => {
    expect(sliceBreadcrumbItems({ account: "gitslice" }, "nic")).toEqual([]);
    expect(sliceBreadcrumbItems(undefined, "nic")).toEqual([]);
  });
});
