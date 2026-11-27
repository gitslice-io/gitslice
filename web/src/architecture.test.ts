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
const DYNAMIC_IMPORT_RE = /\bimport\(\s*["']([^"']+)["']\s*\)/g;
const PACKAGE_IMPORT_RE =
  /(?:^|\n)\s*(?:import|export)\s+(?:type\s+)?(?:[^"'\n]*?\s+from\s+)?["']([^."'][^"']*)["']/g;

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
  for (const match of text.matchAll(DYNAMIC_IMPORT_RE)) {
    const spec = match[1];
    if (spec.startsWith(".")) {
      imports.push(normalizePosix([...dirParts, ...spec.split("/")]));
    }
  }
  return imports;
}

function extractPackageImports(text: string): string[] {
  const imports: string[] = [];
  for (const match of text.matchAll(PACKAGE_IMPORT_RE)) {
    imports.push(match[1]);
  }
  for (const match of text.matchAll(DYNAMIC_IMPORT_RE)) {
    const spec = match[1];
    if (!spec.startsWith(".")) {
      imports.push(spec);
    }
  }
  return imports;
}

describe("web architecture boundaries", () => {
  const entries = Object.entries(sources).map(([key, text]) => ({
    rel: key.replace(/^\/src\//, ""),
    imports: extractRelativeImports(key, text),
    packages: extractPackageImports(text)
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

  it("prevents cross-imports between sibling route feature directories", () => {
    const featureDirs = new Set([
      "changeset-detail",
      "changesets-list",
      "slice-detail",
      "doc",
      "blog",
      "landing",
      "site"
    ]);
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (!rel.startsWith("routes/")) {
        continue;
      }
      const sourceDir = rel.split("/")[1];
      if (!featureDirs.has(sourceDir)) {
        continue;
      }
      for (const imp of imports) {
        if (!imp.startsWith("routes/")) {
          continue;
        }
        const targetDir = imp.split("/")[1];
        if (featureDirs.has(targetDir) && targetDir !== sourceDir) {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("keeps src/routes/loaders free of route UI subdirectories and components", () => {
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (!rel.startsWith("routes/loaders/")) {
        continue;
      }
      for (const imp of imports) {
        if (imp.startsWith("components/")) {
          violations.push(`${rel} -> ${imp}`);
        } else if (imp.startsWith("routes/")) {
          const targetDir = imp.split("/")[1];
          if (targetDir !== "loaders" && targetDir !== "layouts" && targetDir !== "layouts.ts") {
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

  it("keeps src/queries free of src/components and src/routes imports", () => {
    const violations: string[] = [];
    for (const { rel, imports } of entries) {
      if (!rel.startsWith("queries/")) {
        continue;
      }
      for (const imp of imports) {
        if (imp.startsWith("components/") || imp.startsWith("routes/")) {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });

  it("keeps @connectrpc/connect and src/api/client isolated inside src/api", () => {
    const violations: string[] = [];
    for (const { rel, imports, packages } of entries) {
      if (rel.startsWith("api/")) {
        continue;
      }
      for (const pkg of packages) {
        if (pkg === "@connectrpc/connect" || pkg.startsWith("@connectrpc/")) {
          violations.push(`${rel} -> ${pkg}`);
        }
      }
      for (const imp of imports) {
        if (imp === "api/client" || imp === "api/client.ts") {
          violations.push(`${rel} -> ${imp}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });
});
