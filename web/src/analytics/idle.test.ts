// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { afterPageLoad } from "./idle";

function setReadyState(state: DocumentReadyState) {
  Object.defineProperty(document, "readyState", { configurable: true, get: () => state });
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  delete (document as unknown as { readyState?: unknown }).readyState;
  delete (window as unknown as { requestIdleCallback?: unknown }).requestIdleCallback;
});

describe("afterPageLoad", () => {
  it("waits for the browser to be idle once the page has loaded", async () => {
    setReadyState("complete");
    let run: (() => void) | undefined;
    const requestIdleCallback = vi.fn((callback: () => void) => {
      run = callback;
      return 1;
    });
    (window as unknown as { requestIdleCallback: unknown }).requestIdleCallback = requestIdleCallback;
    let done = false;
    void afterPageLoad(2000).then(() => (done = true));

    await Promise.resolve();
    expect(requestIdleCallback).toHaveBeenCalledWith(expect.any(Function), { timeout: 2000 });
    expect(done).toBe(false);
    run?.();
    await Promise.resolve();
    expect(done).toBe(true);
  });

  it("waits for the load event while the page is still loading", async () => {
    setReadyState("loading");
    const requestIdleCallback = vi.fn((callback: () => void) => {
      callback();
      return 1;
    });
    (window as unknown as { requestIdleCallback: unknown }).requestIdleCallback = requestIdleCallback;
    let done = false;
    void afterPageLoad().then(() => (done = true));

    await Promise.resolve();
    expect(requestIdleCallback).not.toHaveBeenCalled();
    expect(done).toBe(false);
    window.dispatchEvent(new Event("load"));
    await Promise.resolve();
    expect(requestIdleCallback).toHaveBeenCalledTimes(1);
    expect(done).toBe(true);
  });

  it("falls back to a short timer where there is no idle callback (Safari)", async () => {
    setReadyState("complete");
    let done = false;
    void afterPageLoad(5000).then(() => (done = true));
    await Promise.resolve();
    expect(done).toBe(false);
    vi.advanceTimersByTime(999);
    await Promise.resolve();
    expect(done).toBe(false);
    vi.advanceTimersByTime(1);
    await Promise.resolve();
    expect(done).toBe(true);
  });
});
