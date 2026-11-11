import { describe, expect, it } from "vitest";

// The API client (Connect, protobuf and the service descriptors) is fetched
// when a call is made, not with the page's first script (./lazyClient). One
// static import of ./client from code a page loads would put it back in that
// script for every visitor. Only the server's client, and the loader itself
// through import(), may name the module; everything else takes RpcError and
// the rest from ./rpcError.
const mayImportClient = new Set(["/src/api/serverApi.ts"]);

const sources = import.meta.glob<string>(["/src/**/*.{ts,tsx}", "!/src/**/*.test.{ts,tsx}", "!/src/gen/**"], {
  eager: true,
  import: "default",
  query: "?raw"
});

describe("the API client's code", () => {
  it("is imported statically only by the server's client", () => {
    const paths = Object.keys(sources);
    expect(paths.length).toBeGreaterThan(100);
    const importers = paths.filter(
      (path) => /from\s+"(?:\.\.?\/)+(?:api\/)?client"/.test(sources[path]) && !mayImportClient.has(path)
    );
    expect(importers).toEqual([]);
  });

  it("is loaded on demand by the lazy client", () => {
    expect(sources["/src/api/lazyClient.ts"]).toContain('import("./client")');
  });
});
