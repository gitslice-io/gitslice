import { describe, expect, it } from "vitest";

import { hasSessionCookie } from "./sessionCookie";

describe("hasSessionCookie", () => {
  it("is false for a request with no cookies, or none of a session", () => {
    expect(hasSessionCookie("")).toBe(false);
    expect(hasSessionCookie("theme=dark; ph_phc_x_posthog=%7B%7D")).toBe(false);
  });

  it("is true for a session token, with or without the instance suffix", () => {
    expect(hasSessionCookie("__session=eyJhbGciOi")).toBe(true);
    expect(hasSessionCookie("a=1; __session_Ab12Cd=eyJ; b=2")).toBe(true);
  });

  it("is true for a client cookie that says someone is signed in, false when it says nobody is", () => {
    expect(hasSessionCookie("__client_uat=1761350000")).toBe(true);
    expect(hasSessionCookie("x=y; __client_uat_Ab12Cd=1761350000")).toBe(true);
    expect(hasSessionCookie("__client_uat=0")).toBe(false);
    expect(hasSessionCookie("__client_uat_Ab12Cd=0; __client_uat=0")).toBe(false);
    // Signed out in one place and signed in in the other still counts.
    expect(hasSessionCookie("__client_uat=0; __session=abc")).toBe(true);
  });

  it("ignores empty values and look-alike names", () => {
    expect(hasSessionCookie("__session=")).toBe(false);
    expect(hasSessionCookie("__client_uat=")).toBe(false);
    expect(hasSessionCookie("my__session=abc; __sessionx=1; x__client_uat=5")).toBe(false);
    expect(hasSessionCookie("novalue; =oops")).toBe(false);
  });
});
