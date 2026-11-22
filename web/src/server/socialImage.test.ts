import { afterEach, describe, expect, it, vi } from "vitest";

import { socialImageResponse } from "./socialImage";

function memoryCache() {
  const store = new Map<string, Response>();
  return {
    store,
    match: vi.fn(async (request: Request) => store.get(request.url)?.clone()),
    put: vi.fn(async (request: Request, response: Response) => {
      store.set(request.url, response);
    })
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("socialImageResponse", () => {
  it("leaves other paths to the app", async () => {
    expect(await socialImageResponse(new Request("https://gitslice.io/slices/a/b"), "https://api.gitslice.io")).toBeNull();
  });

  it("proxies a card from the API once, then serves it from the edge cache", async () => {
    const fetchMock = vi.fn(
      async () => new Response("png-bytes", { headers: { "Content-Type": "image/png", "Cache-Control": "public, max-age=86400" } })
    );
    vi.stubGlobal("fetch", fetchMock);
    const cache = memoryCache();
    const url = "https://gitslice.io/og/slices/sunny/neo.png?v=abc";

    const first = await socialImageResponse(new Request(url), "https://api.gitslice.io/", cache);
    expect(await first?.text()).toBe("png-bytes");
    expect(first?.headers.get("Content-Type")).toBe("image/png");
    expect(fetchMock).toHaveBeenCalledWith("https://api.gitslice.io/og/slices/sunny/neo.png?v=abc", { redirect: "follow" });

    const second = await socialImageResponse(new Request(url), "https://api.gitslice.io", cache);
    expect(await second?.text()).toBe("png-bytes");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const head = await socialImageResponse(new Request(url, { method: "HEAD" }), "https://api.gitslice.io", cache);
    expect(head?.status).toBe(200);
    expect(await head?.text()).toBe("");
  });

  it("proxies account pictures the same way", async () => {
    const fetchMock = vi.fn(async () => new Response("avatar-png", { headers: { "Content-Type": "image/png" } }));
    vi.stubGlobal("fetch", fetchMock);
    const cache = memoryCache();
    const url = "https://gitslice.io/avatars/sunny.png?v=none";

    expect(await (await socialImageResponse(new Request(url), "https://api.gitslice.io", cache))?.text()).toBe("avatar-png");
    expect(fetchMock).toHaveBeenCalledWith("https://api.gitslice.io/avatars/sunny.png?v=none", { redirect: "follow" });
    await socialImageResponse(new Request(url), "https://api.gitslice.io", cache);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not cache a failure, and refuses other methods", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not found", { status: 404 })));
    const cache = memoryCache();
    const missing = await socialImageResponse(new Request("https://gitslice.io/og/nope"), "https://api.gitslice.io", cache);
    expect(missing?.status).toBe(404);
    expect(cache.put).not.toHaveBeenCalled();

    const post = await socialImageResponse(
      new Request("https://gitslice.io/og/default.png", { method: "POST" }),
      "https://api.gitslice.io",
      cache
    );
    expect(post?.status).toBe(405);
  });
});
