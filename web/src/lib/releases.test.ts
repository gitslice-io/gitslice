import { describe, expect, it } from "vitest";

import { releaseRedirect, releaseRedirectTarget, releasesBase } from "./releases";

function target(path: string, method = "GET"): string | null {
  return releaseRedirectTarget(new Request(`https://gitslice.io${path}`, { method }));
}

describe("releaseRedirectTarget", () => {
  it("maps release pages and downloads to the backing store", () => {
    expect(target("/releases")).toBe(releasesBase);
    expect(target("/releases/")).toBe(releasesBase);
    expect(target("/releases/latest")).toBe(`${releasesBase}/latest`);
    expect(target("/releases/latest/download/gs_linux_amd64.tar.gz")).toBe(
      `${releasesBase}/latest/download/gs_linux_amd64.tar.gz`
    );
    expect(target("/releases/latest/download/checksums.txt")).toBe(
      `${releasesBase}/latest/download/checksums.txt`
    );
    expect(target("/releases/download/v0.2.0/gs_darwin_arm64.tar.gz")).toBe(
      `${releasesBase}/download/v0.2.0/gs_darwin_arm64.tar.gz`
    );
    expect(target("/releases/tag/v0.2.0")).toBe(`${releasesBase}/tag/v0.2.0`);
    expect(target("/releases/latest", "HEAD")).toBe(`${releasesBase}/latest`);
  });

  it("rejects anything that could escape the releases tree", () => {
    expect(target("/releases/download/v0.2.0/../../settings")).toBeNull();
    expect(target("/releases/download/..%2F..%2Fsettings/x")).toBeNull();
    expect(target("/releases/latest/download/.hidden")).toBeNull();
    expect(target("/releases/download/v0.2.0")).toBeNull();
    expect(target("/releases/other")).toBeNull();
    expect(target("/releasesx")).toBeNull();
    expect(target("/releases/latest", "POST")).toBeNull();
  });
});

describe("releaseRedirect", () => {
  it("answers with a temporary redirect", () => {
    const response = releaseRedirect(new Request("https://gitslice.io/releases/latest"));
    expect(response?.status).toBe(302);
    expect(response?.headers.get("Location")).toBe(`${releasesBase}/latest`);
  });

  it("passes through other paths", () => {
    expect(releaseRedirect(new Request("https://gitslice.io/doc"))).toBeNull();
  });
});
