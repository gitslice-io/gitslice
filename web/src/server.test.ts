import { afterEach, describe, expect, it, vi } from "vitest";

import { gitProxyTarget } from "./lib/gitProxy";

describe("gitProxyTarget", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("maps git smart-HTTP requests to the API origin", () => {
    vi.stubEnv("VITE_API_BASE_URL", "https://api.gitslice.io/");
    const request = new Request(
      "https://gitslice.io/git/slices/home.git/info/refs?service=git-upload-pack"
    );

    expect(gitProxyTarget(request)).toBe(
      "https://api.gitslice.io/git/slices/home.git/info/refs?service=git-upload-pack"
    );
  });

  it("does not proxy non-git paths", () => {
    vi.stubEnv("VITE_API_BASE_URL", "https://api.gitslice.io");

    expect(
      gitProxyTarget(new Request("https://gitslice.io/slices/home"))
    ).toBeNull();
  });

  it("does not proxy when the API base URL is empty", () => {
    vi.stubEnv("VITE_API_BASE_URL", "");

    expect(
      gitProxyTarget(new Request("https://gitslice.io/git/slices/home.git"))
    ).toBeNull();
  });
});
