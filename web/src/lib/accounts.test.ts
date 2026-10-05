import { describe, expect, it } from "vitest";

import { canAdmin, canWrite, membershipFor, normalizeMemberships } from "./accounts";

describe("memberships", () => {
  it("uses what the server reports", () => {
    const memberships = normalizeMemberships(["nic", "acme"], [
      { account: "nic", kind: "personal", role: "Owner" },
      { account: "acme", kind: "organization", role: "writer" }
    ]);
    expect(memberships).toEqual([
      { account: "nic", kind: "personal", role: "owner" },
      { account: "acme", kind: "organization", role: "writer" }
    ]);
    expect(membershipFor(memberships, "ACME")?.kind).toBe("organization");
    expect(membershipFor(memberships, "other")).toBeUndefined();
  });

  it("falls back to the account names from an older server", () => {
    expect(normalizeMemberships(["nic", "acme"], undefined)).toEqual([
      { account: "nic", kind: "personal", role: "" },
      { account: "acme", kind: "", role: "" }
    ]);
  });

  it("follows the server's role rules", () => {
    const as = (role: string) => ({ account: "a", kind: "organization", role });
    expect(["owner", "admin", "writer", "member"].map((r) => canWrite(as(r)))).toEqual([true, true, true, true]);
    expect(canWrite(as("reader"))).toBe(false);
    expect(canAdmin(as("admin"))).toBe(true);
    expect(canAdmin(as("writer"))).toBe(false);
    expect(canWrite(undefined)).toBe(false);
    // An unreported role is left to the server to decide.
    expect(canWrite(as(""))).toBe(true);
  });
});
