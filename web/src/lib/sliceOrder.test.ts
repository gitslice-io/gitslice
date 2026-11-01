import { describe, expect, it } from "vitest";

import { sortSlices } from "./sliceOrder";

const slice = (name: string, createdAt?: string, lastActivityAt?: string) => ({
  ref: { account: "a", slice: name },
  createdAt,
  lastActivityAt
});

describe("sortSlices", () => {
  const slices = [
    slice("zeta", "2026-03-01T00:00:00Z"),
    slice("alpha", "2026-01-01T00:00:00Z", "2026-05-01T00:00:00Z"),
    slice("home", "2026-01-01T00:00:00Z"),
    slice("beta")
  ];
  const names = (sort: Parameters<typeof sortSlices>[1]) => sortSlices(slices, sort).map((s) => s.ref?.slice);

  it("keeps home first in every order", () => {
    expect(names("name")).toEqual(["home", "alpha", "beta", "zeta"]);
    expect(names("newest")).toEqual(["home", "zeta", "alpha", "beta"]);
    // No activity falls back to the creation date; no dates sort last.
    expect(names("active")).toEqual(["home", "alpha", "zeta", "beta"]);
  });
});
