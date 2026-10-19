import { describe, expect, it } from "vitest";

import { markRepoImages, projectRoot, resolveRepoImage, resolveRepoLink } from "./repoLinks";

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

describe("projectRoot", () => {
  it("is the included path that holds the document, the deepest when several do", () => {
    expect(projectRoot(["/sunny/ryujinx-neo"], "/sunny/ryujinx-neo/docs/README.md")).toBe("/sunny/ryujinx-neo");
    expect(projectRoot(["/sunny", "/sunny/ryujinx-neo"], "/sunny/ryujinx-neo/README.md")).toBe("/sunny/ryujinx-neo");
    expect(projectRoot(["/acme/a", "/acme/b"], "/acme/b/x/README.md")).toBe("/acme/b");
    expect(projectRoot(["/acme/app"], "/acme/app")).toBe("/acme/app");
  });

  it("is not fooled by a sibling with the same beginning, and is null when no path holds the document", () => {
    expect(projectRoot(["/acme/app"], "/acme/application/README.md")).toBeNull();
    expect(projectRoot([], "/acme/app/README.md")).toBeNull();
    expect(projectRoot(undefined, "/acme/app/README.md")).toBeNull();
    expect(projectRoot(["/"], "/acme/app/README.md")).toBe("/");
  });
});

describe("resolveRepoImage", () => {
  it("resolves a root-absolute image against the project root, as GitHub does", () => {
    expect(resolveRepoImage("/sunny/ryujinx-neo/docs", "/sunny/ryujinx-neo", "/Imagens/logo.png")).toBe("/sunny/ryujinx-neo/Imagens/logo.png");
    expect(resolveRepoImage("/sunny/ryujinx-neo", "/sunny/ryujinx-neo", "/a%20b.png?raw=1")).toBe("/sunny/ryujinx-neo/a b.png");
  });

  it("keeps a root-absolute image inside the project", () => {
    expect(resolveRepoImage("/acme/app", "/acme/app", "/../secret.png")).toBeNull();
    expect(resolveRepoImage("/acme/app", "/acme/app", "/a/../../x.png")).toBeNull();
    expect(resolveRepoImage("/acme/app", "/acme/app", "/a/../b.png")).toBe("/acme/app/b.png");
  });

  it("leaves a root-absolute image alone without a project root, and protocol-relative ones always", () => {
    expect(resolveRepoImage("/acme/app", null, "/img/x.png")).toBeNull();
    expect(resolveRepoImage("/acme/app", "/acme/app", "//cdn.example.com/x.png")).toBeNull();
  });

  it("resolves other images as links are", () => {
    expect(resolveRepoImage("/acme/app/docs", "/acme/app", "shot.png")).toBe("/acme/app/docs/shot.png");
    expect(resolveRepoImage("/acme/app/docs", "/acme/app", "../logo.png")).toBe("/acme/app/logo.png");
    expect(resolveRepoImage("/acme/app", "/acme/app", "https://example.com/x.png")).toBeNull();
  });
});

describe("markRepoImages with a project root", () => {
  it("marks root-absolute images with their absolute repository path", () => {
    const html = markRepoImages('<img src="/Imagens/a.png" alt="a"><img src="rel.png" alt="r">', "/sunny/ryujinx-neo/docs", "/sunny/ryujinx-neo");
    const doc = new DOMParser().parseFromString(html, "text/html");
    const [a, rel] = Array.from(doc.querySelectorAll("img"));
    expect(a.getAttribute("data-repo-image")).toBe("/sunny/ryujinx-neo/Imagens/a.png");
    expect(rel.getAttribute("data-repo-image")).toBe("/sunny/ryujinx-neo/docs/rel.png");
  });

  it("leaves a root-absolute image as it is when the document has no project", () => {
    const html = '<img src="/Imagens/a.png" alt="a">';
    expect(markRepoImages(html, "/x", null)).toBe(html);
  });
});
