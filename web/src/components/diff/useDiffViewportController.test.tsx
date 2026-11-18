import "@testing-library/jest-dom/vitest";

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { diffFileId, type DiffFile } from "./parseDiff";
import { useDiffViewportController } from "./useDiffViewportController";

function makeFile(path: string): DiffFile {
  return {
    id: diffFileId(path),
    path,
    changeKind: "modified",
    additions: 1,
    deletions: 0,
    lines: [{ kind: "add", text: "+hello", content: "hello", newNumber: 1 }],
    rows: []
  };
}

describe("useDiffViewportController", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("tracks active file, toggles viewed/collapse state, and requests file diffs on demand", () => {
    const originalIO = globalThis.IntersectionObserver;
    // Simulate environment without IntersectionObserver to verify eager fallback too.
    // @ts-expect-error test stub
    globalThis.IntersectionObserver = undefined;

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const onFileNeeded = vi.fn();

      const { result } = renderHook(() =>
        useDiffViewportController({
          files,
          onFileNeeded
        })
      );

      expect(result.current.currentActiveId).toBe(files[0].id);
      expect(result.current.mountedDiffBodies.has(files[0].id)).toBe(true);
      expect(onFileNeeded).toHaveBeenCalledWith("a.ts");
      expect(onFileNeeded).toHaveBeenCalledWith("b.ts");

      act(() => {
        result.current.selectFile(files[1].id);
      });
      expect(result.current.currentActiveId).toBe(files[1].id);

      expect(result.current.isCollapsed(files[0])).toBe(false);
      act(() => {
        result.current.toggleViewed(files[0]);
      });
      expect(result.current.viewedIds.has(files[0].id)).toBe(true);
      expect(result.current.isCollapsed(files[0])).toBe(true);

      act(() => {
        result.current.toggleCollapsed(files[0]);
      });
      expect(result.current.isCollapsed(files[0])).toBe(false);
    } finally {
      globalThis.IntersectionObserver = originalIO;
    }
  });
});
