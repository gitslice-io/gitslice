import type { Slice, SliceRef, TreeEntry } from "../../../api/types";
import type { ApiClient } from "../../../api/useApi";
import {
  isIncludedRoot,
  isPathNotFoundError,
  isPathWritable,
  normalizeRepositoryPath
} from "../sourceUtils";

import {
  joinRepositoryPath,
  parentRepositoryPath,
  repositoryPathName,
  validateEntryName
} from "./paths";

export type CopyMoveMode = "copy" | "move";

export interface CopyMoveSource {
  path: string;
  isDirectory: boolean;
}

// resolveDestination turns what the person typed into an absolute path.
// A relative path resolves against the source's folder, "." and ".." segments
// are folded away, and a trailing slash means "put it inside that folder".
export function resolveDestination(
  input: string,
  source: CopyMoveSource
): { path: string; into: boolean } | { error: string } {
  const raw = input.trim().replace(/\\/g, "/");
  if (!raw) {
    return { error: "Enter a destination path." };
  }
  const into = raw.endsWith("/");
  const base = raw.startsWith("/")
    ? []
    : normalizeRepositoryPath(parentRepositoryPath(source.path))
        .split("/")
        .filter(Boolean);
  const parts = [...base];
  for (const segment of raw.split("/")) {
    if (segment === "" || segment === ".") {
      continue;
    }
    if (segment === "..") {
      if (!parts.length) {
        return { error: "That path goes above the repository root." };
      }
      parts.pop();
      continue;
    }
    const problem = validateEntryName(segment);
    if (problem) {
      return { error: problem };
    }
    parts.push(segment);
  }
  if (!parts.length) {
    return { error: "Enter a path inside a folder, not the repository root." };
  }
  return { path: `/${parts.join("/")}`, into };
}

// destinationProblems checks a resolved destination against the source and the
// slice, before anything is read from the server. It returns "" when fine.
export function destinationProblem(options: {
  canModify(path: string): boolean;
  destination: string;
  mode: CopyMoveMode;
  source: CopyMoveSource;
}): string {
  const source = normalizeRepositoryPath(options.source.path);
  const destination = normalizeRepositoryPath(options.destination);
  if (destination === source) {
    return options.mode === "move"
      ? "The destination is where it already is."
      : "Choose a different path from the original.";
  }
  if (options.source.isDirectory && destination.startsWith(`${source}/`)) {
    return "A folder can't be placed inside itself.";
  }
  if (!options.canModify(destination)) {
    return "The destination is outside the folders this slice includes. Pick a path inside one of them.";
  }
  return "";
}

// intoFolder is the path an item takes when dropped inside a folder.
export function intoFolder(folder: string, source: CopyMoveSource) {
  return joinRepositoryPath(folder, repositoryPathName(source.path));
}

// existingEntry returns what is at path, or undefined when nothing is.
export async function existingEntry(
  api: ApiClient,
  options: { commitId: string; path: string; slice?: SliceRef }
): Promise<TreeEntry | undefined> {
  try {
    const resolved = await api.resolvePath(options);
    return resolved.entry;
  } catch (error) {
    if (isPathNotFoundError(error)) {
      return undefined;
    }
    throw error;
  }
}

// accountOfPath is the account a repository path belongs to: its first folder.
export function accountOfPath(path: string) {
  return normalizeRepositoryPath(path).split("/").filter(Boolean)[0] ?? "";
}

const SLICE_PAGE_LIMIT = 20;

// findDestinationSlice finds the slice of the destination's account that a
// changeset editing path can go into: the most specific one whose included
// paths cover it. A changeset edits one slice, so a copy that leaves the
// current slice lands in this one.
export async function findDestinationSlice(
  api: ApiClient,
  destination: string
): Promise<Slice | undefined> {
  const account = accountOfPath(destination);
  if (!account) {
    return undefined;
  }
  let best: { slice: Slice; depth: number } | undefined;
  let cursor = "";
  for (let page = 0; page < SLICE_PAGE_LIMIT; page++) {
    const response = await api.listSlices({ account, cursor, pageSize: 100 });
    for (const slice of response.slices ?? []) {
      const included = slice.definition?.includedPaths ?? [];
      if (
        slice.archived ||
        !isPathWritable(included, destination) ||
        isIncludedRoot(included, destination)
      ) {
        continue;
      }
      const depth = Math.max(
        ...included
          .filter((path) => isPathWritable([path], destination))
          .map((path) => normalizeRepositoryPath(path).length)
      );
      if (!best || depth > best.depth) {
        best = { slice, depth };
      }
    }
    cursor = response.nextCursor ?? "";
    if (!cursor) {
      break;
    }
  }
  return best?.slice;
}
