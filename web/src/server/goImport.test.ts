import { describe, expect, it, vi } from "vitest";

import { apiSliceLookup, goImportResponse, goModules, sliceGoImportResponse, type GoModule } from "./goImport";

describe("goImportResponse", () => {
  it("serves the go-import tag for the module root and its packages", async () => {
    for (const path of ["/gitslice", "/gitslice/", "/gitslice/cmd/gs"]) {
      const response = goImportResponse(new Request(`https://gitslice.io${path}?go-get=1`));
      expect(response?.status).toBe(200);
      expect(response?.headers.get("Content-Type")).toContain("text/html");
      const body = await response!.text();
      expect(body).toContain(
        '<meta name="go-import" content="gitslice.io/gitslice git https://gitslice.io/git/gitslice/gitslice.git gitslice/gitslice">'
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

describe("sliceGoImportResponse", () => {
  const lookup = (paths: Record<string, string[]>) =>
    vi.fn(async (account: string, slice: string) => {
      const included = paths[`${account}/${slice}`];
      return included ? { includedPaths: included } : null;
    });

  it("answers for any slice, rooted at its included path", async () => {
    const find = lookup({ "acme/payment": ["/acme/payment"] });
    for (const path of ["/acme/payment", "/acme/payment/internal/charge"]) {
      const response = await sliceGoImportResponse(new Request(`https://gitslice.io${path}?go-get=1`), find);
      expect(response?.status).toBe(200);
      const body = await response!.text();
      expect(body).toContain(
        '<meta name="go-import" content="gitslice.io/acme/payment git https://gitslice.io/git/acme/payment.git acme/payment">'
      );
      expect(body).toContain("https://gitslice.io/slices/acme/payment?path=/acme/payment{/dir}/{file}");
    }
  });

  it("says why there is no module", async () => {
    const find = lookup({ "acme/mono": ["/acme/a", "/acme/b"] });
    const missing = await sliceGoImportResponse(new Request("https://gitslice.io/acme/secret?go-get=1"), find);
    expect(missing?.status).toBe(404);
    expect(await missing!.text()).toMatch(/does not exist or is private.*~\/\.netrc/);
    const multi = await sliceGoImportResponse(new Request("https://gitslice.io/acme/mono?go-get=1"), find);
    expect(multi?.status).toBe(404);
    expect(await multi!.text()).toContain("2 paths");
  });

  it("passes a ~/.netrc login on to the API as a token", async () => {
    const find = lookup({ "acme/private": ["/acme/private"] });
    await sliceGoImportResponse(
      new Request("https://gitslice.io/acme/private?go-get=1", {
        headers: { Authorization: `Basic ${btoa("ann:gs_token_123")}` }
      }),
      find
    );
    expect(find).toHaveBeenCalledWith("acme", "private", "gs_token_123");
  });

  it("leaves other requests alone", async () => {
    const find = lookup({ "acme/payment": ["/acme/payment"] });
    expect(await sliceGoImportResponse(new Request("https://gitslice.io/acme/payment"), find)).toBeNull();
    expect(await sliceGoImportResponse(new Request("https://gitslice.io/acme?go-get=1"), find)).toBeNull();
    expect(await sliceGoImportResponse(new Request("https://gitslice.io/git/acme/payment.git?go-get=1"), find)).toBeNull();
    expect(find).not.toHaveBeenCalled();
  });

  it("looks slices up through ResolveSlice", async () => {
    const fetchFn = vi.fn(async () =>
      new Response(JSON.stringify({ definition: { includedPaths: ["/acme/payment"] } }), { status: 200 })
    ) as unknown as typeof fetch;
    const found = await apiSliceLookup("https://api.gitslice.io/", fetchFn)("acme", "payment", "tok");
    expect(found).toEqual({ includedPaths: ["/acme/payment"] });
    expect(fetchFn).toHaveBeenCalledWith(
      "https://api.gitslice.io/gitslice.core.v1.SliceService/ResolveSlice",
      expect.objectContaining({ headers: expect.objectContaining({ Authorization: "Bearer tok" }) })
    );
    const denied = vi.fn(async () => new Response("{}", { status: 401 })) as unknown as typeof fetch;
    expect(await apiSliceLookup("https://api.gitslice.io", denied)("acme", "secret", null)).toBeNull();
  });
});
