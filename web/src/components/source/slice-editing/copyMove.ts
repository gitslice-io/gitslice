import type { SliceRef, TreeEntry } from "../../../api/types";
import type { ApiClient } from "../../../api/useApi";
import { isPathNotFoundError, normalizeRepositoryPath } from "../sourceUtils";

import {
  joinRepositoryPath,
  parentRepositoryPath,
  repositoryPathName,
  validateEntryName
} from "./paths";
import type { PendingEdit } from "./pendingEdits";

export type CopyMoveMode = "copy" | "move";

export interface CopyMoveSource {
  path: string;
  isDirectory: boolean;
}

// A folder copy stages one edit per file, so cap it: a changeset with
// thousands of staged edits is slow to save and hard to review.
export const COPY_ENTRY_LIMIT = 1000;

const DIRECTORY = "ENTRY_KIND_DIRECTORY";
const FILE = "ENTRY_KIND_FILE";

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

// planMove is a single rename, which keeps history and works for folders.
export function planMove(source: CopyMoveSource, destination: string): PendingEdit[] {
  return [
    {
      kind: "rename",
      oldPath: normalizeRepositoryPath(source.path),
      path: normalizeRepositoryPath(destination)
    }
  ];
}

// planCopy turns the entries under the source into new files and folders under
// the destination. entries are the source itself first (a file), or every
// entry beneath a folder.
export function planCopy(
  source: CopyMoveSource,
  destination: string,
  entries: TreeEntry[]
): PendingEdit[] {
  const from = normalizeRepositoryPath(source.path);
  const to = normalizeRepositoryPath(destination);
  const edits: PendingEdit[] = [];
  if (source.isDirectory) {
    edits.push({ kind: "mkdir", path: to });
  }
  for (const entry of entries) {
    const path = normalizeRepositoryPath(entry.path ?? "");
    const target = source.isDirectory ? to + path.slice(from.length) : to;
    if (entry.kind === DIRECTORY) {
      edits.push({ kind: "mkdir", path: target });
    } else if (entry.kind === FILE && entry.blobId && entry.contentHash) {
      edits.push({
        kind: "write",
        path: target,
        isNew: true,
        blobId: entry.blobId,
        contentHash: entry.contentHash,
        ...(entry.mode ? { mode: entry.mode } : {})
      });
    }
  }
  return edits;
}

// skippedEntries counts what a copy leaves out: symlinks and other entries
// that are neither files nor folders.
export function skippedEntries(entries: TreeEntry[]) {
  return entries.filter(
    (entry) => entry.kind !== DIRECTORY && entry.kind !== FILE
  ).length;
}

export class CopyTooLargeError extends Error {
  constructor() {
    super(
      `That folder has more than ${COPY_ENTRY_LIMIT} items. Copy part of it, or use gs on the command line.`
    );
  }
}

// readCopySource lists what a copy has to duplicate: the file's own entry, or
// everything under the folder (recursively, falling back to a walk when the
// server only lists one level).
export async function readCopySource(
  api: ApiClient,
  options: { commitId: string; slice?: SliceRef; source: CopyMoveSource }
): Promise<TreeEntry[]> {
  const { commitId, slice, source } = options;
  if (!source.isDirectory) {
    const resolved = await api.resolvePath({
      commitId,
      path: source.path,
      slice
    });
    return resolved.entry ? [resolved.entry] : [];
  }
  const entries: TreeEntry[] = [];
  const pending = [normalizeRepositoryPath(source.path)];
  while (pending.length) {
    const directory = pending.shift() as string;
    let cursor = "";
    do {
      const page = await api.listDirectory({
        commitId,
        cursor,
        pageSize: 500,
        path: directory,
        recursive: true,
        slice
      });
      const listed = page.entries ?? [];
      entries.push(...listed);
      if (entries.length > COPY_ENTRY_LIMIT) {
        throw new CopyTooLargeError();
      }
      if (!page.recursive) {
        for (const entry of listed) {
          if (entry.kind === DIRECTORY && entry.path) {
            pending.push(normalizeRepositoryPath(entry.path));
          }
        }
      }
      cursor = page.nextCursor ?? "";
    } while (cursor);
  }
  return entries;
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
