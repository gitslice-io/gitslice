import type { TreeEntry } from "../api/types";

export function normalizeRelativePath(value: string): string {
  const parts: string[] = [];

  for (const part of value.replace(/\\/g, "/").split("/")) {
    const trimmed = part.trim();
    if (!trimmed || trimmed === ".") {
      continue;
    }
    if (trimmed === "..") {
      parts.pop();
      continue;
    }
    parts.push(trimmed);
  }

  return parts.join("/");
}

export function normalizeRepositoryPath(value: string): string {
  const relative = normalizeRelativePath(value);
  return relative ? `/${relative}` : "/";
}

export function entryDisplayName(entry: TreeEntry): string {
  if (entry.name) {
    return entry.name;
  }
  if (entry.path) {
    const parts = normalizeRelativePath(entry.path).split("/");
    return parts[parts.length - 1] || entry.path;
  }
  return "(unnamed)";
}

export function isSliceProjectionDirectoryPath(path: string, includedPaths: string[]): boolean {
  if (!path) {
    return true;
  }

  const normalized = normalizeRepositoryPath(path);

  return includedPaths.some((includedPath) => {
    const includedNormalized = normalizeRepositoryPath(includedPath);
    return (
      normalized === includedNormalized ||
      includedNormalized.startsWith(`${normalized}/`)
    );
  });
}
