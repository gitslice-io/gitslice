// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import { CLERK_UI_ATTRIBUTE, clerkUiInDocument, clerkUiWanted } from "./clerkUi";

describe("clerkUiWanted", () => {
  it("is wanted on the pages that show Clerk's cards", () => {
    for (const path of ["/login", "/login/sso-callback", "/signup", "/signup/verify", "/cli-login"]) {
      expect(clerkUiWanted(path, ""), path).toBe(true);
    }
  });

  it("is not wanted by a visitor with no session on any other page", () => {
    for (const path of ["/", "/doc", "/blogs/x", "/slices/a/b", "/cs/123", "/loginx", "/accounts/login"]) {
      expect(clerkUiWanted(path, "gitslice_welcome=1; __client_uat=0"), path).toBe(false);
    }
  });

  it("is wanted wherever the request may be signed in", () => {
    expect(clerkUiWanted("/slices/a/b", "__session=abc")).toBe(true);
    expect(clerkUiWanted("/", "__client_uat=1790000000")).toBe(true);
  });
});

describe("clerkUiInDocument", () => {
  afterEach(() => {
    document.documentElement.removeAttribute(CLERK_UI_ATTRIBUTE);
  });

  it("is true unless the server said the document has none", () => {
    expect(clerkUiInDocument()).toBe(true);
    document.documentElement.setAttribute(CLERK_UI_ATTRIBUTE, "1");
    expect(clerkUiInDocument()).toBe(true);
    document.documentElement.setAttribute(CLERK_UI_ATTRIBUTE, "0");
    expect(clerkUiInDocument()).toBe(false);
  });
});
