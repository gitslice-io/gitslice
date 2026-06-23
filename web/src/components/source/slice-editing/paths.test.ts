import { afterEach, describe, expect, it, vi } from "vitest";

import {
  joinRepositoryPath,
  parentRepositoryPath,
  repositoryPathName,
  validateEntryName
} from "./paths";

describe("paths", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("joinRepositoryPath", () => {
    it("joins directory path and name", () => {
      expect(joinRepositoryPath("/test", "file.txt")).toBe("/test/file.txt");
    });

    it("handles empty directory path", () => {
      expect(joinRepositoryPath("", "file.txt")).toBe("/file.txt");
    });

    it("handles empty name", () => {
      expect(joinRepositoryPath("/test", "")).toBe("/test");
    });

    it("trims whitespace from name", () => {
      expect(joinRepositoryPath("/test", "  file.txt  ")).toBe("/test/file.txt");
    });

    it("normalizes directory path", () => {
      expect(joinRepositoryPath("/test/", "file.txt")).toBe("/test/file.txt");
    });
  });

  describe("parentRepositoryPath", () => {
    it("returns empty string for root path", () => {
      expect(parentRepositoryPath("/")).toBe("");
    });

    it("returns empty string for single-level path", () => {
      expect(parentRepositoryPath("/file.txt")).toBe("");
    });

    it("returns parent directory for nested path", () => {
      expect(parentRepositoryPath("/test/file.txt")).toBe("/test");
    });

    it("handles deeply nested paths", () => {
      expect(parentRepositoryPath("/a/b/c/d/file.txt")).toBe("/a/b/c/d");
    });
  });

  describe("repositoryPathName", () => {
    it("extracts the name from a path", () => {
      expect(repositoryPathName("/test/file.txt")).toBe("file.txt");
    });

    it("handles root path", () => {
      expect(repositoryPathName("/")).toBe("");
    });

    it("handles single-level path", () => {
      expect(repositoryPathName("/file.txt")).toBe("file.txt");
    });

    it("handles deeply nested paths", () => {
      expect(repositoryPathName("/a/b/c/d/file.txt")).toBe("file.txt");
    });
  });

  describe("validateEntryName", () => {
    it("returns empty string for valid simple name", () => {
      expect(validateEntryName("file.txt")).toBe("");
    });

    it("returns empty string for valid nested path", () => {
      expect(validateEntryName("docs/notes.md")).toBe("");
    });

    it("returns error for empty string", () => {
      expect(validateEntryName("")).toBe("Enter a name.");
    });

    it("returns error for whitespace-only string", () => {
      expect(validateEntryName("   ")).toBe("Enter a name.");
    });

    it("returns error for path with empty segments", () => {
      expect(validateEntryName("/file.txt")).toBe("Path can't have empty segments (no leading, trailing, or double slashes).");
      expect(validateEntryName("file.txt/")).toBe("Path can't have empty segments (no leading, trailing, or double slashes).");
      expect(validateEntryName("docs//notes.md")).toBe("Path can't have empty segments (no leading, trailing, or double slashes).");
    });

    it("returns error for segments with leading/trailing spaces", () => {
      expect(validateEntryName(" docs/ file")).toBe("Path segments can't start or end with a space.");
    });

    it("returns error for '.' segments", () => {
      expect(validateEntryName(".")).toBe("Path segments can't be \".\" or \"..\".");
      expect(validateEntryName("docs/.")).toBe("Path segments can't be \".\" or \"..\".");
    });

    it("returns error for '..' segments", () => {
      expect(validateEntryName("..")).toBe("Path segments can't be \".\" or \"..\".");
      expect(validateEntryName("docs/../notes.md")).toBe("Path segments can't be \".\" or \"..\".");
    });

    it("returns error for control characters", () => {
      expect(validateEntryName("file\x00.txt")).toBe("Path can't contain control characters.");
    });

    it("handles backslashes", () => {
      expect(validateEntryName("docs\\notes.md")).toBe("");
    });
  });
});