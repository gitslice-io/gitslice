import type { TreeEntry } from "../../api/types";
import {
  normalizeRepositoryPath
} from "../../components/source/sourceUtils";

interface GitCloneEnv extends ImportMetaEnv {
  readonly VITE_GITSLICE_GIT_HTTP_BASE_URL?: string;
}

export interface DirectoryLoadResult {
  data: TreeEntry[];
  error: Error | null;
  isLoading: boolean;
  path: string;
}

export interface SourceTreeNode {
  children: Map<string, SourceTreeNode>;
  kind: TreeEntry["kind"];
  name: string;
  path: string;
  synthetic: boolean;
}

export interface SourceTreeRow {
  depth: number;
  node: SourceTreeNode;
}

export function pathSearchValue(value: unknown) {
  return typeof value === "string" && value.trim()
    ? normalizeRepositoryPath(value)
    : "";
}

export function buildGitCloneHint(account?: string, slug?: string, origin?: string) {
  const gitEnv = import.meta.env as GitCloneEnv;
  const envBase = (gitEnv.VITE_GITSLICE_GIT_HTTP_BASE_URL ?? "").replace(
    /\/+$/,
    ""
  );
  // The git smart-HTTP endpoint is served same-origin with the web app (the
  // Worker reverse-proxies /git/* to the backend), so prefer the live origin
  // when the caller supplies it. Fall back to the build-time base for SSR and
  // local dev, where git runs on its own port.
  const baseUrl = (origin ?? "").replace(/\/+$/, "") || envBase;
  const clonePath = `/git/${encodeURIComponent(
    account || "account"
  )}/${encodeURIComponent(slug || "slice")}.git`;

  return {
    configured: Boolean(baseUrl),
    url: baseUrl
      ? `${baseUrl}${clonePath}`
      : `http://<git-http-host>${clonePath}`
  };
}

export function buildSourceTree(
  includedPaths: string[],
  directoryResults: DirectoryLoadResult[]
) {
  const root: SourceTreeNode = {
    children: new Map(),
    kind: "ENTRY_KIND_DIRECTORY",
    name: "slice root",
    path: "",
    synthetic: false
  };
  const nodes = new Map<string, SourceTreeNode>([["", root]]);

  function ensureNode(
    rawPath: string,
    kind: TreeEntry["kind"] = "ENTRY_KIND_DIRECTORY",
    name?: string,
    synthetic = true
  ) {
    const path = normalizeTreePath(rawPath);
    if (!path) {
      return root;
    }

    const parentPath = parentRepositoryPath(path);
    const parent = ensureNode(parentPath);
    let node = nodes.get(path);
    if (!node) {
      node = {
        children: new Map(),
        kind,
        name: name ?? repositoryPathName(path),
        path,
        synthetic
      };
      nodes.set(path, node);
      parent.children.set(path, node);
    } else {
      if (kind) {
        node.kind = kind;
      }
      node.name = name ?? node.name;
      node.synthetic = node.synthetic && synthetic;
    }
    return node;
  }

  for (const includedPath of includedPaths) {
    for (const path of ancestorDirectoryPaths(normalizeTreePath(includedPath))) {
      ensureNode(path);
    }
    ensureNode(includedPath);
  }

  for (const result of directoryResults) {
    ensureNode(result.path);
    for (const entry of result.data) {
      const path = normalizeTreePath(entry.path ?? "");
      if (!path) {
        continue;
      }
      ensureNode(path, entry.kind, entryDisplayName(entry), false);
    }
  }

  return root;
}

export function flattenTreeRows(
  root: SourceTreeNode,
  expandedPaths: Set<string>
): SourceTreeRow[] {
  if (!expandedPaths.has("")) {
    return [];
  }

  const rows: SourceTreeRow[] = [];

  function visit(node: SourceTreeNode, depth: number) {
    const children = Array.from(node.children.values()).sort(compareTreeNodes);
    for (const child of children) {
      rows.push({ depth, node: child });
      if (child.kind === "ENTRY_KIND_DIRECTORY" && expandedPaths.has(child.path)) {
        visit(child, depth + 1);
      }
    }
  }

  visit(root, 0);
  return rows;
}

export function initialTreeExpansion(
  selectedPath: string,
  includedPaths: string[],
  isSelectedDirectory: boolean
) {
  const paths = new Set<string>([""]);
  const selectedExpansion = isSelectedDirectory
    ? ancestorDirectoryPaths(selectedPath)
    : parentAncestorDirectoryPaths(selectedPath);
  for (const path of selectedExpansion) {
    paths.add(path);
  }
  for (const includedPath of includedPaths) {
    for (const path of ancestorDirectoryPaths(normalizeTreePath(includedPath))) {
      paths.add(path);
    }
  }
  return paths;
}

export function parentAncestorDirectoryPaths(path: string) {
  return ancestorDirectoryPaths(parentRepositoryPath(path));
}

export function ancestorDirectoryPaths(path: string) {
  const normalized = normalizeTreePath(path);
  const parts = normalized.split("/").filter(Boolean);
  const paths = [""];

  for (let index = 1; index < parts.length; index += 1) {
    paths.push(`/${parts.slice(0, index).join("/")}`);
  }

  if (normalized && !paths.includes(normalized)) {
    paths.push(normalized);
  }

  return paths;
}

export function parentRepositoryPath(path: string) {
  const parts = normalizeTreePath(path).split("/").filter(Boolean);
  if (parts.length <= 1) {
    return "";
  }
  return `/${parts.slice(0, -1).join("/")}`;
}

export function repositoryPathName(path: string) {
  const parts = normalizeTreePath(path).split("/").filter(Boolean);
  return parts[parts.length - 1] ?? "slice root";
}

export function normalizeTreePath(path: string) {
  const normalized = normalizeRepositoryPath(path);
  return normalized === "/" ? "" : normalized;
}

export function compareTreeNodes(left: SourceTreeNode, right: SourceTreeNode) {
  if (left.kind !== right.kind) {
    if (left.kind === "ENTRY_KIND_DIRECTORY") return -1;
    if (right.kind === "ENTRY_KIND_DIRECTORY") return 1;
  }
  return left.name.localeCompare(right.name, undefined, {
    numeric: true,
    sensitivity: "base"
  });
}

export function compareRepositoryPaths(left: string, right: string) {
  return left.localeCompare(right, undefined, {
    numeric: true,
    sensitivity: "base"
  });
}

function entryDisplayName(entry: TreeEntry) {
  if (entry.name) {
    return entry.name;
  }
  if (entry.path) {
    const parts = normalizeRepositoryPath(entry.path).split("/").filter(Boolean);
    return parts[parts.length - 1] ?? entry.path;
  }
  return "(unnamed)";
}