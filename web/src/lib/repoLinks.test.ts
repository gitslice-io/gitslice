import { describe, expect, it } from "vitest";

import { resolveRepoLink } from "./repoLinks";

describe("resolveRepoLink", () => {
  it("resolves links against the document's directory", () => {
    expect(resolveRepoLink("/acme/app", "docs/guide.md")).toBe("/acme/app/docs/guide.md");
    expect(resolveRepoLink("/acme/app", "./docs/")).toBe("/acme/app/docs");
    expect(resolveRepoLink("/acme/app/docs", "../LICENSE")).toBe("/acme/app/LICENSE");
    expect(resolveRepoLink("/acme/app", "a//b/./c.md")).toBe("/acme/app/a/b/c.md");
  });

  it("drops queries and anchors, and decodes the path", () => {
    expect(resolveRepoLink("/acme/app", "guide.md#install")).toBe("/acme/app/guide.md");
    expect(resolveRepoLink("/acme/app", "my%20notes.md?plain=1")).toBe("/acme/app/my notes.md");
    expect(resolveRepoLink("/acme/app", "100%.md")).toBe("/acme/app/100%.md");
  });

  it("leaves everything that is not a repository path alone", () => {
    for (const href of ["", "#top", "/cs/abc123", "//example.com/x", "https://example.com", "mailto:a@b.c", "javascript:alert(1)", "?q=1"]) {
      expect(resolveRepoLink("/acme/app", href)).toBeNull();
    }
    expect(resolveRepoLink("/acme/app", null)).toBeNull();
    expect(resolveRepoLink("/acme/app", undefined)).toBeNull();
  });

  it("refuses links that climb out of the repository", () => {
    expect(resolveRepoLink("/acme", "../../x")).toBeNull();
    expect(resolveRepoLink("", "../x")).toBeNull();
    expect(resolveRepoLink("/acme", "..")).toBe("/");
  });
});
