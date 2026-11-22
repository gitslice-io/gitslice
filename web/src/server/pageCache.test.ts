import { describe, expect, it, vi } from "vitest";

import {
  PAGE_CACHE_HEADER,
  PAGE_FRESH_MS,
  PAGE_STALE_MS,
  pageCacheKey,
  servePage,
  storablePage,
  type PageCacheStore
} from "./pageCache";

const html = { accept: "text/html,application/xhtml+xml" };

function get(path: string, headers: Record<string, string> = html, method = "GET") {
  return new Request(`https://gitslice.io${path}`, { headers, method });
}

describe("pageCacheKey", () => {
  it("covers the pages read from the API, and nothing else", () => {
    for (const path of ["/slices/a/b", "/slices/a/b?path=%2Fx", "/cs/1234", "/changesets?slice=a:b", "/accounts/a"]) {
      expect(pageCacheKey(get(path), "b1"), path).toBeDefined();
    }
    for (const path of ["/", "/doc", "/login", "/slices/new", "/slices/a/b/settings", "/accounts/a/secrets", "/git/a/b.git", "/assets/x.js"]) {
      expect(pageCacheKey(get(path), "b1"), path).toBeUndefined();
    }
  });

  it("never covers a request that may be signed in", () => {
    expect(pageCacheKey(get("/slices/a/b", { ...html, cookie: "__session=abc" }), "b1")).toBeUndefined();
    expect(pageCacheKey(get("/slices/a/b", { ...html, cookie: "__client_uat=1790000000" }), "b1")).toBeUndefined();
    expect(pageCacheKey(get("/slices/a/b", { ...html, authorization: "Bearer x" }), "b1")).toBeUndefined();
  });

  it("leaves out other methods, non-page requests and Clerk's own addresses", () => {
    expect(pageCacheKey(get("/slices/a/b", html, "POST"), "b1")).toBeUndefined();
    expect(pageCacheKey(get("/slices/a/b", { accept: "*/*" }), "b1")).toBeUndefined();
    expect(pageCacheKey(get("/slices/a/b?__clerk_handshake=x"), "b1")).toBeUndefined();
  });

  it("differs by build, by address and by whether the visitor banner shows", () => {
    const base = pageCacheKey(get("/slices/a/b?path=%2Fx&z=1"), "b1");
    expect(pageCacheKey(get("/slices/a/b?z=1&path=%2Fx"), "b1")).toBe(base);
    expect(pageCacheKey(get("/slices/a/b?path=%2Fx&z=1"), "b2")).not.toBe(base);
    expect(pageCacheKey(get("/slices/a/b?path=%2Fy&z=1"), "b1")).not.toBe(base);
    expect(pageCacheKey(get("/slices/a/b?path=%2Fx&z=1", { ...html, cookie: "gitslice_welcome=1" }), "b1")).not.toBe(base);
    // A signed-out browser's own cookies do not split the cache.
    expect(pageCacheKey(get("/slices/a/b?path=%2Fx&z=1", { ...html, cookie: "__client_uat=0; ph_x=1" }), "b1")).toBe(base);
  });
});

describe("storablePage", () => {
  it("keeps only a complete HTML page that sets no cookie", () => {
    const page = (status: number, headers: Record<string, string>) => new Response("x", { headers, status });
    expect(storablePage(page(200, { "content-type": "text/html; charset=utf-8" }))).toBe(true);
    expect(storablePage(page(404, { "content-type": "text/html" }))).toBe(false);
    expect(storablePage(page(200, { "content-type": "application/json" }))).toBe(false);
    expect(storablePage(page(200, { "content-type": "text/html", "set-cookie": "a=b" }))).toBe(false);
  });
});

function memoryCache() {
  const entries = new Map<string, { body: string; headers: [string, string][] }>();
  const cache: PageCacheStore = {
    async match(key) {
      const entry = entries.get(key);
      return entry ? new Response(entry.body, { headers: entry.headers }) : undefined;
    },
    async put(key, response) {
      entries.set(key, { body: await response.text(), headers: [...response.headers.entries()] });
    }
  };
  return { cache, entries };
}

function setup() {
  const { cache, entries } = memoryCache();
  let clock = 1_000_000;
  let renders = 0;
  const background: Promise<unknown>[] = [];
  const options = {
    buildId: "b1",
    cache,
    now: () => clock,
    render: vi.fn(async () => {
      renders += 1;
      return new Response(`render ${renders}`, { headers: { "content-type": "text/html" } });
    }),
    waitUntil: (work: Promise<unknown>) => {
      background.push(work);
    }
  };
  return {
    advance: (ms: number) => {
      clock += ms;
    },
    entries,
    options,
    settle: () => Promise.all(background.splice(0)),
    async serve(request = get("/slices/a/b")) {
      const response = await servePage(request, options);
      return { body: await response.text(), response, state: response.headers.get(PAGE_CACHE_HEADER) };
    }
  };
}

describe("servePage", () => {
  it("renders once, then serves the copy while it is fresh", async () => {
    const t = setup();
    expect(await t.serve()).toMatchObject({ body: "render 1", state: "miss" });
    await t.settle();
    t.advance(PAGE_FRESH_MS - 1);
    const second = await t.serve();
    expect(second).toMatchObject({ body: "render 1", state: "hit" });
    expect(second.response.headers.get("cache-control")).toBe("private, no-cache");
    expect(second.response.headers.get("x-gitslice-page-stored-at")).toBeNull();
    expect(t.options.render).toHaveBeenCalledTimes(1);
  });

  it("serves a stale copy at once and replaces it in the background", async () => {
    const t = setup();
    await t.serve();
    await t.settle();
    t.advance(PAGE_FRESH_MS + 1);
    expect(await t.serve()).toMatchObject({ body: "render 1", state: "stale" });
    await t.settle();
    expect(await t.serve()).toMatchObject({ body: "render 2", state: "hit" });
  });

  it("renders again once the copy is too old to show", async () => {
    const t = setup();
    await t.serve();
    await t.settle();
    t.advance(PAGE_STALE_MS + 1);
    expect(await t.serve()).toMatchObject({ body: "render 2", state: "miss" });
  });

  it("renders for itself any request that may be signed in, and stores nothing", async () => {
    const t = setup();
    await t.serve();
    await t.settle();
    const signedIn = await t.serve(get("/slices/a/b", { ...html, cookie: "__session=abc" }));
    expect(signedIn).toMatchObject({ body: "render 2", state: null });
    expect(t.entries.size).toBe(1);
  });

  it("does not keep a page that is not a plain 200, or that sets a cookie", async () => {
    const t = setup();
    t.options.render.mockImplementation(async () => new Response("gone", { headers: { "content-type": "text/html" }, status: 404 }));
    expect((await t.serve()).response.status).toBe(404);
    t.options.render.mockImplementation(
      async () => new Response("x", { headers: { "content-type": "text/html", "set-cookie": "a=b" } })
    );
    await t.serve();
    await t.settle();
    expect(t.entries.size).toBe(0);
  });

  it("falls back to rendering when the cache fails or is missing", async () => {
    const t = setup();
    t.options.cache.match = async () => {
      throw new Error("cache down");
    };
    expect(await t.serve()).toMatchObject({ body: "render 1", state: null });
    const response = await servePage(get("/slices/a/b"), { ...t.options, cache: undefined });
    expect(await response.text()).toBe("render 2");
  });

  it("without a way to work in the background, uses a copy only while fresh", async () => {
    const t = setup();
    const options = { ...t.options, waitUntil: undefined };
    await servePage(get("/slices/a/b"), options);
    t.advance(PAGE_FRESH_MS + 1);
    const response = await servePage(get("/slices/a/b"), options);
    expect(response.headers.get(PAGE_CACHE_HEADER)).toBe("miss");
  });
});
