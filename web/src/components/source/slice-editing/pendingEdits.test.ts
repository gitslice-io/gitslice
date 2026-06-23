import { afterEach, describe, expect, it, vi } from "vitest";

import type { PendingEdit } from "./pendingEdits";
import {
  upsertPendingEdit,
  pendingEditKey,
  removePendingEdit,
  pendingWriteForPath,
  pendingDeleteForPath,
  pendingRenameForPath,
  pendingChildrenForDirectory
} from "./pendingEdits";

describe("pendingEdits", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("pendingEditKey", () => {
    it("generates a unique key for write edits", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "test" };
      expect(pendingEditKey(edit)).toBe("write:/test/file.txt");
    });

    it("generates a unique key for mkdir edits", () => {
      const edit: PendingEdit = { kind: "mkdir", path: "/test/folder" };
      expect(pendingEditKey(edit)).toBe("mkdir:/test/folder");
    });

    it("generates a unique key for delete edits", () => {
      const edit: PendingEdit = { kind: "delete", path: "/test/file.txt" };
      expect(pendingEditKey(edit)).toBe("delete:/test/file.txt");
    });

    it("generates a unique key for rename edits based on oldPath", () => {
      const edit: PendingEdit = { kind: "rename", oldPath: "/test/old.txt", path: "/test/new.txt" };
      expect(pendingEditKey(edit)).toBe("rename:/test/old.txt");
    });
  });

  describe("upsertPendingEdit", () => {
    it("adds a new edit to an empty list", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true };
      const result = upsertPendingEdit([], edit);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual(edit);
    });

    it("replaces an existing edit with the same key", () => {
      const existingEdit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "old" };
      const newEdit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "new" };
      const result = upsertPendingEdit([existingEdit], newEdit);
      expect(result).toHaveLength(1);
      const writeResult = result[0] as { kind: "write"; path: string; isNew: boolean; content?: string };
      expect(writeResult.content).toBe("new");
    });

    it("removes edits that conflict with a rename", () => {
      const existingWrite: PendingEdit = { kind: "write", path: "/test/old.txt", isNew: true, content: "test" };
      const renameEdit: PendingEdit = { kind: "rename", oldPath: "/test/old.txt", path: "/test/new.txt" };
      const result = upsertPendingEdit([existingWrite], renameEdit);
      expect(result).toHaveLength(1);
      expect(result[0].kind).toBe("rename");
    });
  });

  describe("removePendingEdit", () => {
    it("removes an edit by key", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "test" };
      const result = removePendingEdit([edit], "write:/test/file.txt");
      expect(result).toHaveLength(0);
    });

    it("preserves other edits", () => {
      const edit1: PendingEdit = { kind: "write", path: "/test/file1.txt", isNew: true, content: "test1" };
      const edit2: PendingEdit = { kind: "write", path: "/test/file2.txt", isNew: true, content: "test2" };
      const result = removePendingEdit([edit1, edit2], "write:/test/file1.txt");
      expect(result).toHaveLength(1);
      expect(result[0].path).toBe("/test/file2.txt");
    });
  });

  describe("pendingWriteForPath", () => {
    it("finds a write edit for the given path", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: false, content: "test" };
      const result = pendingWriteForPath([edit], "/test/file.txt");
      expect(result).toEqual(edit);
    });

    it("returns undefined for non-write edits", () => {
      const edit: PendingEdit = { kind: "delete", path: "/test/file.txt" };
      const result = pendingWriteForPath([edit], "/test/file.txt");
      expect(result).toBeUndefined();
    });

    it("normalizes the path before searching", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "test" };
      const result = pendingWriteForPath([edit], "/test//file.txt");
      expect(result).toEqual(edit);
    });
  });

  describe("pendingDeleteForPath", () => {
    it("finds a delete edit for the given path", () => {
      const edit: PendingEdit = { kind: "delete", path: "/test/file.txt" };
      const result = pendingDeleteForPath([edit], "/test/file.txt");
      expect(result).toEqual(edit);
    });

    it("returns undefined for non-delete edits", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "test" };
      const result = pendingDeleteForPath([edit], "/test/file.txt");
      expect(result).toBeUndefined();
    });
  });

  describe("pendingRenameForPath", () => {
    it("finds a rename edit where oldPath matches", () => {
      const edit: PendingEdit = { kind: "rename", oldPath: "/test/old.txt", path: "/test/new.txt" };
      const result = pendingRenameForPath([edit], "/test/old.txt");
      expect(result).toEqual(edit);
    });

    it("returns undefined for non-rename edits", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "test" };
      const result = pendingRenameForPath([edit], "/test/file.txt");
      expect(result).toBeUndefined();
    });
  });

  describe("pendingChildrenForDirectory", () => {
    it("finds new write edits in the directory", () => {
      const edit1: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: true, content: "test" };
      const edit2: PendingEdit = { kind: "write", path: "/test/file2.txt", isNew: true, content: "test2" };
      const result = pendingChildrenForDirectory([edit1, edit2], "/test");
      expect(result).toHaveLength(2);
    });

    it("finds mkdir edits in the directory", () => {
      const edit: PendingEdit = { kind: "mkdir", path: "/test/folder" };
      const result = pendingChildrenForDirectory([edit], "/test");
      expect(result).toHaveLength(1);
      expect(result[0].kind).toBe("mkdir");
    });

    it("does not include existing write edits (isNew: false)", () => {
      const edit: PendingEdit = { kind: "write", path: "/test/file.txt", isNew: false, content: "test" };
      const result = pendingChildrenForDirectory([edit], "/test");
      expect(result).toHaveLength(0);
    });

    it("does not include edits in other directories", () => {
      const edit: PendingEdit = { kind: "write", path: "/other/file.txt", isNew: true, content: "test" };
      const result = pendingChildrenForDirectory([edit], "/test");
      expect(result).toHaveLength(0);
    });
  });
});