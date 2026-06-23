import type { Changeset, FileEdit, SliceRef } from "../../../api/types";
import { type ApiClient } from "../../../api/useApi";

import type { PendingEdit } from "./pendingEdits";
import { pendingEditKey } from "./pendingEdits";
import { normalizeRepositoryPath } from "../sourceUtils";

export async function preparePendingFileEdits(
  api: ApiClient,
  edits: PendingEdit[],
  slice: SliceRef
): Promise<FileEdit[]> {
  const uploadedWrites = new Map<string, FileEdit>();
  for (const edit of edits) {
    if (edit.kind !== "write") {
      continue;
    }

    if (edit.content === undefined) {
      if (!edit.blobId || !edit.contentHash) {
        throw new Error(`Draft write for ${edit.path} is missing blob metadata.`);
      }
      uploadedWrites.set(pendingEditKey(edit), {
        op: edit.isNew ? "add" : "update",
        path: edit.path,
        blobId: edit.blobId,
        contentHash: edit.contentHash
      });
      continue;
    }

    const uploaded = await api.uploadBlob({
      data: utf8ToBase64(edit.content),
      slice
    });
    if (!uploaded.blobId || !uploaded.contentHash) {
      throw new Error(`Upload did not return blob metadata for ${edit.path}.`);
    }
    uploadedWrites.set(pendingEditKey(edit), {
      op: edit.isNew ? "add" : "update",
      path: edit.path,
      blobId: uploaded.blobId,
      contentHash: uploaded.contentHash
    });
  }

  const ordered = [...edits].sort(compareEditOrder);
  return ordered.map((edit) => {
    if (edit.kind === "write") {
      const fileEdit = uploadedWrites.get(pendingEditKey(edit));
      if (!fileEdit) {
        throw new Error(`Missing uploaded blob for ${edit.path}.`);
      }
      return fileEdit;
    }
    if (edit.kind === "mkdir") {
      return { op: "mkdir", path: edit.path };
    }
    if (edit.kind === "rename") {
      return { op: "rename", oldPath: edit.oldPath, path: edit.path };
    }
    return { op: "delete", path: edit.path };
  });
}

export function compareEditOrder(left: PendingEdit, right: PendingEdit) {
  return editOrder(left.kind) - editOrder(right.kind);
}

function editOrder(kind: PendingEdit["kind"]) {
  switch (kind) {
    case "mkdir":
      return 0;
    case "write":
      return 1;
    case "rename":
      return 2;
    case "delete":
      return 3;
  }
}

export function currentChangesetPatchset(changeset: Changeset) {
  if (!changeset.patchsets?.length) {
    return undefined;
  }
  return (
    changeset.patchsets.find(
      (patchset) => patchset.id === changeset.currentPatchsetId
    ) ?? changeset.patchsets[changeset.patchsets.length - 1]
  );
}

export function fileEditToPendingEdit(edit: FileEdit): PendingEdit | null {
  const op = edit.op ?? "";
  const rawPath = edit.path?.trim() ?? "";

  if (!rawPath) {
    return null;
  }

  const path = normalizeRepositoryPath(rawPath);

  if (
    op === "add" ||
    op === "update" ||
    op === "modify" ||
    op === "upsert"
  ) {
    return {
      kind: "write",
      path,
      isNew: op === "add",
      blobId: edit.blobId,
      contentHash: edit.contentHash
    };
  }

  if (op === "mkdir") {
    return { kind: "mkdir", path };
  }

  if (op === "rename") {
    const rawOldPath = edit.oldPath?.trim() ?? "";
    if (!rawOldPath) {
      return null;
    }
    const oldPath = normalizeRepositoryPath(rawOldPath);
    return { kind: "rename", oldPath, path };
  }

  if (op === "delete") {
    return { kind: "delete", path };
  }

  return null;
}

export function draftStatusLabel(status: "idle" | "adopting" | "saving" | "saved" | "failed") {
  if (status === "adopting") {
    return "Loading draft...";
  }
  if (status === "saving") {
    return "Saving...";
  }
  if (status === "saved") {
    return "Saved";
  }
  if (status === "failed") {
    return "Save failed";
  }
  return "Not saved";
}

export function errorMessageFromUnknown(error: unknown) {
  return error instanceof Error ? error.message : "The request failed.";
}

export function pendingEditLabel(edit: PendingEdit) {
  if (edit.kind === "write") {
    return edit.isNew ? "new file" : "edited";
  }
  if (edit.kind === "mkdir") {
    return "new folder";
  }
  if (edit.kind === "rename") {
    return "renamed";
  }
  return "deleted";
}

function utf8ToBase64(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}