import { describe, expect, it } from "vitest";

import { dismissalCookie, visitorBannerHiddenOn, visitorBannerWanted } from "./visitorBanner";

describe("visitorBannerWanted", () => {
  it("wants the banner for a visitor with no session who has not dismissed it", () => {
    expect(visitorBannerWanted("")).toBe(true);
    expect(visitorBannerWanted("theme=dark; __client_uat=0")).toBe(true);
    expect(visitorBannerWanted("gitslice_welcome=0")).toBe(true);
  });

  it("does not for someone who dismissed it", () => {
    expect(visitorBannerWanted("gitslice_welcome=1")).toBe(false);
    expect(visitorBannerWanted("a=b; gitslice_welcome=1; c=d")).toBe(false);
    // Another cookie's name that ends the same is not it.
    expect(visitorBannerWanted("x_gitslice_welcome=1")).toBe(true);
  });

  it("does not for anyone who may be signed in", () => {
    expect(visitorBannerWanted("__session=eyJ")).toBe(false);
    expect(visitorBannerWanted("__client_uat=1761350000")).toBe(false);
    expect(visitorBannerWanted("__client_uat_Ab12=1761350000; theme=dark")).toBe(false);
  });
});

describe("dismissalCookie", () => {
  it("remembers for thirty days across the site, and is Secure over https", () => {
    expect(dismissalCookie(false)).toBe("gitslice_welcome=1; Max-Age=2592000; Path=/; SameSite=Lax");
    expect(dismissalCookie(true)).toBe("gitslice_welcome=1; Max-Age=2592000; Path=/; SameSite=Lax; Secure");
    // What it sets is what it looks for.
    expect(visitorBannerWanted(dismissalCookie(false).split(";")[0])).toBe(false);
  });
});

describe("visitorBannerHiddenOn", () => {
  it("leaves out the docs and the blog, which are about Gitslice already", () => {
    for (const path of ["/doc", "/doc/cli", "/blogs", "/blogs/lazy-workspaces", "/blog"]) {
      expect(visitorBannerHiddenOn(path), path).toBe(true);
    }
    for (const path of ["/slices/a/b", "/cs/abc", "/accounts/x", "/changesets", "/documents", "/docs-not"]) {
      expect(visitorBannerHiddenOn(path), path).toBe(false);
    }
  });
});
