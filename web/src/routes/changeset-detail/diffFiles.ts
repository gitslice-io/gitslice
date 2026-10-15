import type { FileEdit, Patchset } from "../../api/types";
import {
  diffFileId,
  parseDiff,
  type DiffFile,
  type FileChangeKind
} from "../../components/diff/parseDiff";

export function samePath(a: string, b: string) {
  return a.replace(/^\/+/, "") === b.replace(/^\/+/, "");
}

export function changedPathsForDiff(from?: Patchset, to?: Patchset) {
  const paths = new Set<string>();
  [from, to].forEach((patchset) => {
    patchset?.fileEdits?.forEach((edit) => {
      if (edit.path) {
        paths.add(edit.path);
      }
      if (edit.oldPath) {
        paths.add(edit.oldPath);
      }
    });
  });
  return Array.from(paths).sort();
}

export function fileMetadataForPath(
  path: string,
  from?: Patchset,
  to?: Patchset
): { changeKind?: FileChangeKind; oldPath?: string } {
  const edit =
    findEditForPath(to?.fileEdits, path) ??
    findEditForPath(from?.fileEdits, path);
  if (!edit) {
    return {};
  }

  return {
    changeKind: changeKindForEdit(edit),
    oldPath:
      edit.op === "rename" && edit.path === path ? edit.oldPath : undefined
  };
}

function findEditForPath(edits: FileEdit[] | undefined, path: string) {
  return edits?.find((edit) => edit.path === path || edit.oldPath === path);
}

function changeKindForEdit(edit: FileEdit): FileChangeKind | undefined {
  switch (edit.op) {
    case "delete":
      return "deleted";
    case "rename":
      return "renamed";
    default:
      return undefined;
  }
}

export function diffFileForResponse(
  path: string,
  metadataKind: FileChangeKind | undefined,
  diff: string | undefined
): DiffFile {
  const parsed = parseDiff(diff ?? "", [path])[0];
  if (!parsed) {
    return {
      additions: 0,
      changeKind: metadataKind ?? "modified",
      deletions: 0,
      id: diffFileId(path),
      lines: [],
      path,
      rows: []
    };
  }

  return {
    ...parsed,
    changeKind: parsed.changeKind,
    id: diffFileId(path),
    path
  };
}
