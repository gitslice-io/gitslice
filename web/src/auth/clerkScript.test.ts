import { describe, expect, it } from "vitest";

import { clerkScriptPreload } from "./clerkScript";

// "clerk.example.com$" as a publishable key.
const key = `pk_live_${btoa("clerk.example.com$")}`;

describe("clerkScriptPreload", () => {
  it("names Clerk's script on the instance's own host, as Clerk's script tag does", () => {
    const link = clerkScriptPreload(key);
    expect(link).toMatchObject({ as: "script", crossOrigin: "anonymous", rel: "preload" });
    expect(link?.href).toMatch(/^https:\/\/clerk\.example\.com\/npm\/@clerk\/clerk-js@\d+\/dist\/clerk\.browser\.js$/);
  });

  it("is nothing without a usable key", () => {
    expect(clerkScriptPreload(undefined)).toBeUndefined();
    expect(clerkScriptPreload("")).toBeUndefined();
  });
});
