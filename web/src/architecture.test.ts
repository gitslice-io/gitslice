import { describe, expect, it } from "vitest";

const sources = import.meta.glob<string>(
  ["/src/**/*.{ts,tsx}", "!/src/**/*.{test,spec}.{ts,tsx}", "!/src/gen/**"],
  {
    eager: true,
    import: "default",
    query: "?raw"
  }
);

const IMPORT_SPECIFIER_RE =
  /(?:^|\n)\s*(?:import|export)\s+(?:type\s+)?(?:[^"'\n]*?\s+from\s+)?["'](\.[^"']+)["']/g;

function normalizePosix(segments: string[]): string {
  const stack: string[] = [];
  for (const part of segments) {
    if (!part || part === ".") {
      continue;
    }
    if (part === "..") {
      stack.pop();
    } else {
      stack.push(part);
    }
  }
  return stack.join("/");
}

function extractRelativeImports(sourceKey: string, text: string): string[] {
  // sourceKey looks like "/src/lib/errors.ts"
  const relFile = sourceKey.replace(/^\/src\//, "");
  const dirParts = relFile.split("/").slice(0, -1);
  const imports: string[] = [];
  for (const match of text.matchAll(IMPORT_SPECIFIER_RE)) {
    const spec = match[1];
    imports.push(normalizePosix([...dirParts, ...spec.split("/")]));
  }
  return imports;
}

describe("web architecture boundaries", () => {
  const entries = Object.entries(sources).map(([key, text]) => ({
    rel: key.replace(/^\/src\//, ""),
    imports: extractRelativeImports(key, text)
  }));

  it("keeps src/lib free of components, routes, and server-only modules", () => {
    expect(entries.length).toBeGreaterThan(100);
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (!rel.startsWith("lib/")) {
        continue;
      }
      for (const imp of imports) {
        if (imp.startsWith("components/") || imp.startsWith("routes/") || imp.startsWith("server/")) {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("keeps src/components/ui free of domain components and routes", () => {
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (!rel.startsWith("components/ui/")) {
        continue;
      }
      for (const imp of imports) {
        if (imp.startsWith("routes/") || (imp.startsWith("components/") && !imp.startsWith("components/ui/"))) {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("keeps src/components free of src/routes imports", () => {
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (!rel.startsWith("components/")) {
        continue;
      }
      for (const imp of imports) {
        if (imp.startsWith("routes/")) {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("restricts src/server imports to server entries and serverApi", () => {
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (rel === "server.ts" || rel.startsWith("server/") || rel === "api/serverApi.ts") {
        continue;
      }
      for (const imp of imports) {
        if (imp.startsWith("server/")) {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("prevents cross-imports between slice-detail and changeset-detail routes", () => {
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (rel.startsWith("routes/slice-detail/")) {
        for (const imp of imports) {
          if (imp.startsWith("routes/changeset-detail/")) {
            violations.push(`${rel} -> ${imp}`);
          }
        }
      } else if (rel.startsWith("routes/changeset-detail/")) {
        for (const imp of imports) {
          if (imp.startsWith("routes/slice-detail/")) {
            violations.push(`${rel} -> ${imp}`);
          }
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("keeps React Query key arrays centralized in src/queries", () => {
    const violations: string[] = [];
    for (const [key, text] of Object.entries(sources)) {
      const rel = key.replace(/^\/src\//, "");
      if (rel.startsWith("queries/")) {
        continue;
      }
      if (/queryKey\s*:\s*\[/.test(text)) {
        violations.push(rel);
      }
    }
    expect(violations).toEqual([]);
  });
});
