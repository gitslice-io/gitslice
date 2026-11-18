import "@testing-library/jest-dom/vitest";

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useMediaQuery } from "./useMediaQuery";

describe("useMediaQuery", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("returns false when matchMedia is unavailable and reacts to media query changes", () => {
    vi.stubGlobal("matchMedia", undefined);
    const { result: unsupported } = renderHook(() => useMediaQuery("(min-width: 1024px)"));
    expect(unsupported.current).toBe(false);

    let matches = false;
    const listeners = new Set<() => void>();
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches,
      media: query,
      addEventListener: (_event: string, fn: () => void) => listeners.add(fn),
      removeEventListener: (_event: string, fn: () => void) => listeners.delete(fn)
    }));

    const { result, unmount } = renderHook(() => useMediaQuery("(min-width: 1024px)"));
    expect(result.current).toBe(false);

    act(() => {
      matches = true;
      listeners.forEach((fn) => fn());
    });
    expect(result.current).toBe(true);

    unmount();
    expect(listeners.size).toBe(0);
  });
});
