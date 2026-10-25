import { describe, expect, it } from "vitest";

// The router must not import a page statically: a page imported at the top of
// router.tsx is in the main chunk, downloaded by every visitor whichever page
// they open. Pages are loaded on demand with lazyRouteComponent. These pages
// are small and needed on every page, so they stay in the main chunk.
const stayInMainChunk = new Set(["ChooseUsernamePage", "NotFoundPage"]);

// The router's source, as text.
const sources = import.meta.glob<string>("./router.tsx", { eager: true, import: "default", query: "?raw" });
const source = Object.values(sources)[0] ?? "";

describe("router.tsx", () => {
  it("is found", () => {
    expect(source).toContain("lazyRouteComponent");
  });

  it("imports no page statically", () => {
    const staticPageImports = [...source.matchAll(/^import\s[^;]*?from\s+"\.\/(\w+Page)";/gms)].map((match) => match[1]);
    const leaked = staticPageImports.filter((name) => !stayInMainChunk.has(name));
    expect(leaked).toEqual([]);
  });

  it("imports nothing but small helpers from the page modules' folders", () => {
    // A helper that lives in a page's own file drags the page in with it.
    expect(source).not.toMatch(/from\s+"\.\/\w+Page"[^;]*sortedPatchsets|sortedPatchsets[^;]*from\s+"\.\/ChangesetDetailPage"/);
  });

  it("loads every routed page on demand", () => {
    const routed = [...source.matchAll(/component:\s+lazyPages\.(\w+)/g)].map((match) => match[1]);
    const lazy = [...source.matchAll(/(\w+):\s+lazyRouteComponent\(\(\) => import\("\.\/(\w+)"\), "(\w+)"\)/g)];
    expect(lazy.length).toBeGreaterThan(10);
    for (const [, key, module, exportName] of lazy) {
      expect(module, `${key} imports its own module`).toBe(key);
      expect(exportName, `${key} exports itself`).toBe(key);
    }
    const defined = new Set(lazy.map((match) => match[1]));
    for (const name of routed) {
      expect(defined.has(name), `${name} is in the lazy table`).toBe(true);
    }
  });
});
