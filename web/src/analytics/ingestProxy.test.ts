import { afterEach, describe, expect, it, vi } from "vitest";

import { handleIngestProxy } from "./ingestProxy";

function mockFetch() {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(new Response("ok", { status: 200 }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("handleIngestProxy", () => {
  it("ignores non-ingest paths", async () => {
    const fetchMock = mockFetch();
    const result = await handleIngestProxy(
      new Request("https://gitslice.io/slices/acme/app"),
    );
    expect(result).toBeUndefined();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("proxies events to the ingestion host and strips the prefix + cookies", async () => {
    const fetchMock = mockFetch();
    const response = await handleIngestProxy(
      new Request("https://gitslice.io/ingest/i/v0/e/?ip=1", {
        method: "POST",
        headers: { cookie: "__session=secret", "content-type": "text/plain" },
        body: "batch",
      }),
    );

    expect(response?.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const proxied = fetchMock.mock.calls[0][0] as Request;
    expect(proxied.url).toBe("https://us.i.posthog.com/i/v0/e/?ip=1");
    expect(proxied.method).toBe("POST");
    expect(proxied.headers.get("cookie")).toBeNull();
  });

  it("proxies static assets to the assets host and caches them", async () => {
    const fetchMock = mockFetch();
    const response = await handleIngestProxy(
      new Request("https://gitslice.io/ingest/static/array.js"),
    );

    const proxied = fetchMock.mock.calls[0][0] as Request;
    expect(proxied.url).toBe("https://us-assets.i.posthog.com/static/array.js");
    expect(response?.headers.get("Cache-Control")).toBe(
      "public, max-age=86400",
    );
  });
});
