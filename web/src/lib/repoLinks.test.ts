import { describe, expect, it } from "vitest";

import { markRepoImages, resolveRepoLink } from "./repoLinks";

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

describe("markRepoImages", () => {
  it("takes the src off repository images, so the browser never requests them from the page", () => {
    const html = markRepoImages(
      '<p><img src="logo.png" alt="logo"> <img src="../shots/a%20b.webp" alt="a"> <img src="https://example.com/x.png" alt="x"> <img src="data:image/png;base64,AAAA" alt="d"></p>',
      "/acme/app/docs"
    );
    const doc = new DOMParser().parseFromString(html, "text/html");
    const [logo, shot, external, data] = Array.from(doc.querySelectorAll("img"));
    expect(logo.hasAttribute("src")).toBe(false);
    expect(logo.getAttribute("data-repo-image")).toBe("/acme/app/docs/logo.png");
    expect(shot.hasAttribute("src")).toBe(false);
    expect(shot.getAttribute("data-repo-image")).toBe("/acme/app/shots/a b.webp");
    // Anything with a scheme stays as it is.
    expect(external.getAttribute("src")).toBe("https://example.com/x.png");
    expect(data.getAttribute("src")).toBe("data:image/png;base64,AAAA");
    expect(external.hasAttribute("data-repo-image")).toBe(false);
  });

  it("leaves markup without repository images untouched", () => {
    const html = '<p>no images <a href="x.md">here</a></p>';
    expect(markRepoImages(html, "/acme/app")).toBe(html);
    const external = '<img src="https://example.com/x.png">';
    expect(markRepoImages(external, "/acme/app")).toBe(external);
  });

  it("drops srcset, which the browser would also request", () => {
    const doc = new DOMParser().parseFromString(markRepoImages('<img src="a.png" srcset="a@2x.png 2x">', "/r"), "text/html");
    expect(doc.querySelector("img")?.hasAttribute("srcset")).toBe(false);
  });
});
