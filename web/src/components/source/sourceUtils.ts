import { RpcError } from "../../api/client";
import type { ApiClient } from "../../api/useApi";
import type {
  ListDirectoryResponse,
  Slice,
  SliceRef,
  TreeEntry
} from "../../api/types";

export interface SourceRouteParams {
  account?: string;
  _splat?: string;
  $?: string;
}

export interface SourceSearchParams {
  commit?: unknown;
  account?: unknown;
}

export interface BreadcrumbSegment {
  label: string;
  routePath: string;
  repositoryPath: string;
}

export function searchString(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

export function routeSplat(params: SourceRouteParams) {
  return normalizeRelativePath(params._splat ?? params.$ ?? "");
}

export function buildRepositoryPath(account: string, routePath: string) {
  const cleanAccount = account.trim().replace(/^\/+|\/+$/g, "");
  const cleanRoutePath = normalizeRelativePath(routePath);

  if (!cleanAccount) {
    return "/";
  }

  return cleanRoutePath ? `/${cleanAccount}/${cleanRoutePath}` : `/${cleanAccount}`;
}

export function repositoryPathToRoutePath(account: string, repositoryPath = "") {
  const cleanAccount = account.trim().replace(/^\/+|\/+$/g, "");
  const normalizedPath = normalizeRepositoryPath(repositoryPath);
  const accountPrefix = `/${cleanAccount}`;

  if (!cleanAccount) {
    return normalizedPath.replace(/^\/+/, "");
  }

  if (normalizedPath === accountPrefix) {
    return "";
  }

  if (normalizedPath.startsWith(`${accountPrefix}/`)) {
    return normalizedPath.slice(accountPrefix.length + 1);
  }

  return normalizedPath.replace(/^\/+/, "");
}

export function breadcrumbSegments(account: string, repositoryPath: string) {
  const routePath = repositoryPathToRoutePath(account, repositoryPath);
  const parts = routePath ? routePath.split("/") : [];
  const segments: BreadcrumbSegment[] = [
    {
      label: account,
      routePath: "",
      repositoryPath: buildRepositoryPath(account, "")
    }
  ];

  let currentRoutePath = "";
  for (const part of parts) {
    currentRoutePath = currentRoutePath ? `${currentRoutePath}/${part}` : part;
    segments.push({
      label: part,
      routePath: currentRoutePath,
      repositoryPath: buildRepositoryPath(account, currentRoutePath)
    });
  }

  return segments;
}

export function normalizeRepositoryPath(value: string) {
  const relative = normalizeRelativePath(value);
  return relative ? `/${relative}` : "/";
}

export function normalizeRelativePath(value: string) {
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

export function entryDisplayName(entry: TreeEntry) {
  if (entry.name) {
    return entry.name;
  }
  if (entry.path) {
    const parts = normalizeRelativePath(entry.path).split("/");
    return parts[parts.length - 1] || entry.path;
  }
  return "(unnamed)";
}

export function entryKindLabel(kind: TreeEntry["kind"]) {
  switch (kind) {
    case "ENTRY_KIND_DIRECTORY":
      return "directory";
    case "ENTRY_KIND_FILE":
      return "file";
    case "ENTRY_KIND_SYMLINK":
      return "symlink";
    default:
      return "unknown";
  }
}

export function formatMode(mode: number | undefined) {
  return typeof mode === "number" ? `0${mode.toString(8)}` : "";
}

export function formatSize(size: string | undefined) {
  if (!size) {
    return "";
  }

  const parsed = Number(size);
  if (!Number.isFinite(parsed)) {
    return size;
  }

  if (parsed < 1024) {
    return `${parsed} B`;
  }

  const units = ["KB", "MB", "GB", "TB"];
  let value = parsed / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }

  return `${value.toFixed(value >= 10 ? 0 : 1)} ${units[unitIndex]}`;
}

export function trimMiddle(value: string, maxLength = 28) {
  if (value.length <= maxLength) {
    return value;
  }

  const edge = Math.floor((maxLength - 1) / 2);
  return `${value.slice(0, edge)}...${value.slice(-edge)}`;
}

export function languageFromPath(path: string) {
  const lower = path.toLowerCase();

  if (lower.endsWith(".tsx")) return "tsx";
  if (lower.endsWith(".ts")) return "ts";
  if (lower.endsWith(".jsx")) return "jsx";
  if (lower.endsWith(".js") || lower.endsWith(".mjs") || lower.endsWith(".cjs")) return "js";
  if (lower.endsWith(".go")) return "go";
  if (lower.endsWith(".json")) return "json";
  if (lower.endsWith(".md") || lower.endsWith(".markdown")) return "markdown";
  if (lower.endsWith(".yaml") || lower.endsWith(".yml")) return "yaml";
  if (lower.endsWith(".html") || lower.endsWith(".htm")) return "html";
  if (lower.endsWith(".css")) return "css";
  if (lower.endsWith(".scss")) return "scss";
  if (lower.endsWith(".sql")) return "sql";
  if (lower.endsWith(".sh") || lower.endsWith(".bash")) return "bash";
  if (lower.endsWith(".py")) return "python";
  if (lower.endsWith(".rs")) return "rust";
  if (lower.endsWith(".java")) return "java";
  if (lower.endsWith(".kt") || lower.endsWith(".kts")) return "kotlin";
  if (lower.endsWith(".proto")) return "proto";
  if (lower.endsWith(".toml")) return "toml";
  if (lower.endsWith(".xml")) return "xml";

  return "text";
}

export function decodeBase64File(data: string | undefined) {
  if (!data) {
    return "";
  }

  try {
    const binary = window.atob(data);
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
    return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
  } catch {
    return data;
  }
}

export async function listDirectoryAll(
  api: ApiClient,
  request: {
    allowMissingDirectory?: boolean;
    commitId: string;
    path: string;
    slice?: SliceRef;
  }
) {
  const entries: TreeEntry[] = [];
  let cursor = "";

  do {
    let page: ListDirectoryResponse;
    try {
      page = await api.listDirectory({
        commitId: request.commitId,
        cursor,
        pageSize: 500,
        path: request.path,
        slice: request.slice
      });
    } catch (error) {
      if (request.allowMissingDirectory && isPathNotFoundError(error)) {
        return [];
      }
      throw error;
    }
    entries.push(...(page.entries ?? []));
    cursor = page.nextCursor ?? "";
  } while (cursor);

  return entries;
}

export function syntheticDirectoryEntry(path: string, fallbackName = "slice root"): TreeEntry {
  const parts = normalizeRepositoryPath(path).split("/").filter(Boolean);

  return {
    kind: "ENTRY_KIND_DIRECTORY",
    name: parts[parts.length - 1] ?? fallbackName,
    path
  };
}

export function isSliceProjectionDirectoryPath(path: string, includedPaths: string[]) {
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

export function isPathNotFoundError(error: unknown) {
  if (!(error instanceof Error)) {
    return false;
  }
  if (error instanceof RpcError) {
    return (
      error.status === 404 ||
      error.code === 5 ||
      error.code === "5" ||
      error.code === "NotFound"
    );
  }
  return error.message.toLowerCase().includes("path not found");
}

export function sliceLabel(slice: Slice) {
  const account = slice.ref?.account;
  const name = slice.ref?.slice;
  if (account && name) {
    return `${account}/${name}`;
  }
  return slice.id ?? "unknown slice";
}

export function coveringSlices(slices: Slice[], repositoryPath: string) {
  const currentPath = normalizeRepositoryPath(repositoryPath);

  return slices.filter((slice) =>
    (slice.definition?.includedPaths ?? []).some((includedPath) =>
      pathCoversCurrentPath(includedPath, currentPath)
    )
  );
}

export function pathCoversCurrentPath(includedPath: string, currentPath: string) {
  const normalizedIncluded = normalizeRepositoryPath(includedPath);
  if (normalizedIncluded === "/") {
    return true;
  }
  return currentPath === normalizedIncluded || currentPath.startsWith(`${normalizedIncluded}/`);
}

export function isPathWritable(includedPaths: string[], path: string): boolean {
  const normalizedPath = normalizeRepositoryPath(path);
  return includedPaths.some((includedPath) =>
    pathCoversCurrentPath(includedPath, normalizedPath)
  );
}

export function isIncludedRoot(includedPaths: string[], path: string): boolean {
  const normalizedPath = normalizeRepositoryPath(path);
  return includedPaths.some(
    (includedPath) => normalizeRepositoryPath(includedPath) === normalizedPath
  );
}

export function canCreateInPath(includedPaths: string[], dir: string): boolean {
  return isPathWritable(includedPaths, dir);
}

export function canModifyPath(includedPaths: string[], path: string): boolean {
  return isPathWritable(includedPaths, path) && !isIncludedRoot(includedPaths, path);
}

export function sortEntries(entries: TreeEntry[]) {
  return [...entries].sort((left, right) => {
    const leftIsDirectory = left.kind === "ENTRY_KIND_DIRECTORY";
    const rightIsDirectory = right.kind === "ENTRY_KIND_DIRECTORY";
    if (leftIsDirectory !== rightIsDirectory) {
      return leftIsDirectory ? -1 : 1;
    }
    return entryDisplayName(left).localeCompare(entryDisplayName(right));
  });
}
