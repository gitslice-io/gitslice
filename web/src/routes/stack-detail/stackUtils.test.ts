import { describe, expect, it } from "vitest";

import type { ChangesetStack } from "../../api/types";
import {
  base64ToUtf8,
  hasInProgressEntry,
  parentRepositoryPath,
  utf8ToBase64
} from "./stackUtils";

describe("stackUtils", () => {
  describe("hasInProgressEntry", () => {
    it("returns false when stack is undefined", () => {
      expect(hasInProgressEntry(undefined)).toBe(false);
    });

    it("returns false when stack has no entries", () => {
      const stack: ChangesetStack = {
        id: "stk_test",
        title: "test stack",
        status: "open",
        createdAt: "2026-06-23T00:00:00Z",
        updatedAt: "2026-06-23T00:00:00Z",
        baseCommitId: "commit_base",
        entries: []
      };
      expect(hasInProgressEntry(stack)).toBe(false);
    });

    it("returns false when all entries are terminal", () => {
      const stack: ChangesetStack = {
        id: "stk_test",
        title: "test stack",
        status: "open",
        createdAt: "2026-06-23T00:00:00Z",
        updatedAt: "2026-06-23T00:00:00Z",
        baseCommitId: "commit_base",
        entries: [
          {
            changesetId: "cs_1",
            depth: "0",
            displayOrder: "1",
            siblingOrder: "1",
            stackId: "stk_test",
            changeset: {
              id: "cs_1",
              title: "test",
              author: "alice",
              authoringSlice: { account: "acme", slice: "payment" },
              baseCommitId: "commit_base",
              baseKind: "commit",
              status: "submitted",
              targetRef: "refs/global/main",
              handle: "acme:payment@1",
              affectedPaths: [],
              currentPatchsetId: "ps_1",
              currentPatchsetNumber: "1",
              patchsets: [
                {
                  id: "ps_1",
                  number: "1",
                  baseCommitId: "commit_base",
                  baseKind: "commit",
                  changesetId: "cs_1",
                  fileEdits: [],
                  changedPaths: []
                }
              ]
            }
          }
        ]
      };
      expect(hasInProgressEntry(stack)).toBe(false);
    });

    it("returns true when an entry is pending_publish", () => {
      const stack: ChangesetStack = {
        id: "stk_test",
        title: "test stack",
        status: "open",
        createdAt: "2026-06-23T00:00:00Z",
        updatedAt: "2026-06-23T00:00:00Z",
        baseCommitId: "commit_base",
        entries: [
          {
            changesetId: "cs_1",
            depth: "0",
            displayOrder: "1",
            siblingOrder: "1",
            stackId: "stk_test",
            changeset: {
              id: "cs_1",
              title: "test",
              author: "alice",
              authoringSlice: { account: "acme", slice: "payment" },
              baseCommitId: "commit_base",
              baseKind: "commit",
              status: "pending_publish",
              targetRef: "refs/global/main",
              handle: "acme:payment@1",
              affectedPaths: [],
              currentPatchsetId: "ps_1",
              currentPatchsetNumber: "1",
              patchsets: [
                {
                  id: "ps_1",
                  number: "1",
                  baseCommitId: "commit_base",
                  baseKind: "commit",
                  changesetId: "cs_1",
                  fileEdits: [],
                  changedPaths: []
                }
              ]
            }
          }
        ]
      };
      expect(hasInProgressEntry(stack)).toBe(true);
    });

    it("returns true when an entry is non-terminal", () => {
      const stack: ChangesetStack = {
        id: "stk_test",
        title: "test stack",
        status: "open",
        createdAt: "2026-06-23T00:00:00Z",
        updatedAt: "2026-06-23T00:00:00Z",
        baseCommitId: "commit_base",
        entries: [
          {
            changesetId: "cs_1",
            depth: "0",
            displayOrder: "1",
            siblingOrder: "1",
            stackId: "stk_test",
            changeset: {
              id: "cs_1",
              title: "test",
              author: "alice",
              authoringSlice: { account: "acme", slice: "payment" },
              baseCommitId: "commit_base",
              baseKind: "commit",
              status: "draft",
              targetRef: "refs/global/main",
              handle: "acme:payment@1",
              affectedPaths: [],
              currentPatchsetId: "ps_1",
              currentPatchsetNumber: "1",
              patchsets: [
                {
                  id: "ps_1",
                  number: "1",
                  baseCommitId: "commit_base",
                  baseKind: "commit",
                  changesetId: "cs_1",
                  fileEdits: [],
                  changedPaths: []
                }
              ]
            }
          }
        ]
      };
      expect(hasInProgressEntry(stack)).toBe(true);
    });
  });

  describe("utf8ToBase64 and base64ToUtf8", () => {
    it("round-trips simple text", () => {
      const original = "hello world";
      const encoded = utf8ToBase64(original);
      const decoded = base64ToUtf8(encoded);
      expect(decoded).toBe(original);
    });

    it("round-trips unicode text", () => {
      const original = "Hello 世界 🌍";
      const encoded = utf8ToBase64(original);
      const decoded = base64ToUtf8(encoded);
      expect(decoded).toBe(original);
    });

    it("round-trips multiline text", () => {
      const original = "line1\nline2\nline3";
      const encoded = utf8ToBase64(original);
      const decoded = base64ToUtf8(encoded);
      expect(decoded).toBe(original);
    });

    it("produces valid base64", () => {
      const original = "test";
      const encoded = utf8ToBase64(original);
      expect(encoded).toBe("dGVzdA==");
    });
  });

  describe("parentRepositoryPath", () => {
    it("returns root for root path", () => {
      expect(parentRepositoryPath("/")).toBe("/");
    });

    it("returns root for top-level directory", () => {
      expect(parentRepositoryPath("/acme")).toBe("/");
    });

    it("returns parent for nested path", () => {
      expect(parentRepositoryPath("/acme/payment")).toBe("/acme");
    });

    it("returns parent for deeply nested path", () => {
      expect(parentRepositoryPath("/acme/payment/parser")).toBe("/acme/payment");
    });

    it("handles trailing slashes", () => {
      expect(parentRepositoryPath("/acme/payment/")).toBe("/acme");
    });

    it("handles multiple leading slashes", () => {
      expect(parentRepositoryPath("//acme/payment")).toBe("/acme");
    });

    it("handles multiple trailing slashes", () => {
      expect(parentRepositoryPath("/acme/payment//")).toBe("/acme");
    });
  });
});