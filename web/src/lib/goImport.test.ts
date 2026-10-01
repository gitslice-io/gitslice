import { describe, expect, it } from "vitest";

import { goImportResponse, goModules, type GoModule } from "./goImport";

describe("goImportResponse", () => {
  it("serves the go-import tag for the module root and its packages", async () => {
    for (const path of ["/gitslice", "/gitslice/", "/gitslice/cmd/gs"]) {
      const response = goImportResponse(new Request(`https://gitslice.io${path}?go-get=1`));
      expect(response?.status).toBe(200);
      expect(response?.headers.get("Content-Type")).toContain("text/html");
      const body = await response!.text();
      expect(body).toContain(
        '<meta name="go-import" content="gitslice.io/gitslice git https://github.com/gitslice-io/gitslice">'
      );
      expect(body).toContain('<meta name="go-source" content="gitslice.io/gitslice ');
    }
  });

  it("ignores requests that are not go-get discovery for a known module", () => {
    expect(goImportResponse(new Request("https://gitslice.io/gitslice/cmd/gs"))).toBeNull();
    expect(goImportResponse(new Request("https://gitslice.io/gitslice?go-get=0"))).toBeNull();
    expect(goImportResponse(new Request("https://gitslice.io/gitslicex?go-get=1"))).toBeNull();
    expect(goImportResponse(new Request("https://gitslice.io/doc?go-get=1"))).toBeNull();
    expect(
      goImportResponse(new Request("https://gitslice.io/gitslice?go-get=1", { method: "POST" }))
    ).toBeNull();
  });

  it("answers HEAD without a body", async () => {
    const response = goImportResponse(
      new Request("https://gitslice.io/gitslice?go-get=1", { method: "HEAD" })
    );
    expect(response?.status).toBe(200);
    expect(await response!.text()).toBe("");
  });

  it("emits the subdirectory field and escapes attribute values", async () => {
    const modules: GoModule[] = [
      {
        importPrefix: "gitslice.io/gitslice",
        vcs: "git",
        repoURL: "https://gitslice.io/git/gitslice/gitslice.git?a=1&b=\"2\"",
        subdir: "gitslice/gitslice"
      }
    ];
    const response = goImportResponse(
      new Request("https://gitslice.io/gitslice/cmd/gs?go-get=1"),
      modules
    );
    const body = await response!.text();
    expect(body).toContain(
      'content="gitslice.io/gitslice git https://gitslice.io/git/gitslice/gitslice.git?a=1&amp;b=&quot;2&quot; gitslice/gitslice"'
    );
    expect(body).not.toContain("go-source");
  });

  it("declares the gitslice module", () => {
    expect(goModules.map((module) => module.importPrefix)).toContain("gitslice.io/gitslice");
  });
});
