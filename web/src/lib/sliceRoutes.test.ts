import { describe, expect, it } from "vitest";

import { sliceBreadcrumbItems } from "./sliceRoutes";

describe("sliceBreadcrumbItems", () => {
  it("names the viewer's own account too, as theirs", () => {
    const items = sliceBreadcrumbItems({ account: "nic", slice: "notes" }, ["nic", "acme"]);

    expect(items.map((item) => item.label)).toEqual(["@nic", "notes"]);
    expect(items[0].title).toBe("Your account");
    expect(items[0].to).toBe("/accounts/$account");
  });

  it("ignores case when comparing accounts", () => {
    expect(sliceBreadcrumbItems({ account: "Nic", slice: "notes" }, ["nic"])[0].title).toBe("Your account");
  });

  it("says when the owner is an organization the viewer belongs to", () => {
    const items = sliceBreadcrumbItems({ account: "acme", slice: "payment" }, ["nic", "acme"]);

    expect(items.map((item) => item.label)).toEqual(["@acme", "payment"]);
    expect(items[0].title).toBe("acme, an account you belong to");
  });

  it("names the owner for someone else's slice", () => {
    const items = sliceBreadcrumbItems({ account: "gitslice", slice: "gitslice" }, ["nic"]);

    expect(items.map((item) => item.label)).toEqual(["@gitslice", "gitslice"]);
    expect(items[0].title).toBe("Owned by gitslice");
    expect(items[0].to).toBe("/accounts/$account");
    expect(items[0].params).toEqual({ account: "gitslice" });
    expect(items[1].to).toBe("/slices/$account/$slice");
  });

  it("names the owner for a visitor who is not signed in", () => {
    expect(sliceBreadcrumbItems({ account: "gitslice", slice: "gitslice" }, []).map((item) => item.label)).toEqual([
      "@gitslice",
      "gitslice"
    ]);
  });

  it("has nothing for an incomplete reference", () => {
    expect(sliceBreadcrumbItems({ account: "gitslice" }, ["nic"])).toEqual([]);
    expect(sliceBreadcrumbItems(undefined, ["nic"])).toEqual([]);
  });
});
