import { normalizeRepositoryPath } from "../sourceUtils";
import { parentRepositoryPath } from "./paths";

export type PendingEdit =
  | {
      kind: "write";
      path: string;
      content?: string;
      isNew: boolean;
      blobId?: string;
      contentHash?: string;
    }
  | { kind: "mkdir"; path: string }
  | { kind: "rename"; oldPath: string; path: string }
  | { kind: "delete"; path: string };

export function upsertPendingEdit(edits: PendingEdit[], edit: PendingEdit) {
  const normalized = normalizePendingEdit(edit);
  const normalizedKey = pendingEditKey(normalized);

  return [
    ...edits.filter(
      (candidate) =>
        pendingEditKey(candidate) !== normalizedKey &&
        !hasConflictingPendingPath(candidate, normalized)
    ),
    normalized
  ];
}

export function pendingEditKey(edit: PendingEdit) {
  if (edit.kind === "rename") {
    return `rename:${normalizeRepositoryPath(edit.oldPath)}`;
  }
  return `${edit.kind}:${normalizeRepositoryPath(edit.path)}`;
}

export function removePendingEdit(edits: PendingEdit[], key: string) {
  return edits.filter((edit) => pendingEditKey(edit) !== key);
}

export function pendingWriteForPath(edits: PendingEdit[], path: string) {
  const normalizedPath = normalizeRepositoryPath(path);
  return edits.find(
    (edit) => edit.kind === "write" && edit.path === normalizedPath
  ) as Extract<PendingEdit, { kind: "write" }> | undefined;
}

export function pendingDeleteForPath(edits: PendingEdit[], path: string) {
  const normalizedPath = normalizeRepositoryPath(path);
  return edits.find(
    (edit) => edit.kind === "delete" && edit.path === normalizedPath
  ) as Extract<PendingEdit, { kind: "delete" }> | undefined;
}

export function pendingRenameForPath(edits: PendingEdit[], path: string) {
  const normalizedPath = normalizeRepositoryPath(path);
  return edits.find(
    (edit) => edit.kind === "rename" && edit.oldPath === normalizedPath
  ) as Extract<PendingEdit, { kind: "rename" }> | undefined;
}

export function pendingChildrenForDirectory(edits: PendingEdit[], directoryPath: string) {
  const normalizedDirectory = normalizeDirectoryPath(directoryPath);
  return edits.filter((edit) => {
    if (edit.kind === "write" && edit.isNew) {
      return parentRepositoryPath(edit.path) === normalizedDirectory;
    }
    if (edit.kind === "mkdir") {
      return parentRepositoryPath(edit.path) === normalizedDirectory;
    }
    return false;
  }) as Array<
    Extract<PendingEdit, { kind: "write" }> | Extract<PendingEdit, { kind: "mkdir" }>
  >;
}

function normalizePendingEdit(edit: PendingEdit): PendingEdit {
  if (edit.kind === "rename") {
    return {
      ...edit,
      oldPath: normalizeRepositoryPath(edit.oldPath),
      path: normalizeRepositoryPath(edit.path)
    };
  }
  return {
    ...edit,
    path: normalizeRepositoryPath(edit.path)
  };
}

function hasConflictingPendingPath(candidate: PendingEdit, next: PendingEdit) {
  if (next.kind === "rename") {
    if (candidate.kind === "rename") {
      return candidate.oldPath === next.oldPath;
    }
    return candidate.path === next.oldPath;
  }

  if (candidate.kind === "rename") {
    return candidate.oldPath === next.path;
  }

  return candidate.path === next.path;
}

function normalizeDirectoryPath(path: string) {
  const normalized = normalizeRepositoryPath(path);
  return normalized === "/" ? "" : normalized;
}