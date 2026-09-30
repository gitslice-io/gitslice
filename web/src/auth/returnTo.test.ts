import { afterEach, describe, expect, it } from "vitest";

import { clearReturnTo, peekReturnTo, rememberReturnTo, safeReturnPath } from "./returnTo";

describe("returnTo", () => {
  afterEach(() => sessionStorage.clear());

  it("accepts only same-origin, non-login paths", () => {
    expect(safeReturnPath("/slices/heibot/home")).toBe("/slices/heibot/home");
    expect(safeReturnPath("/claims?x=1")).toBe("/claims?x=1");
    for (const bad of ["", "https://evil.example", "//evil.example", "/\\evil", "/login", "/login/sso-callback", "relative"]) {
      expect(safeReturnPath(bad)).toBeNull();
    }
  });

  it("remembers until cleared", () => {
    rememberReturnTo("/claims");
    expect(peekReturnTo()).toBe("/claims");
    clearReturnTo();
    expect(peekReturnTo()).toBeNull();
  });
});
