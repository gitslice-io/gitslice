import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const posthogMock = vi.hoisted(() => ({
  alias: vi.fn(),
  capture: vi.fn(),
  get_distinct_id: vi.fn(() => "anon"),
  identify: vi.fn(),
  init: vi.fn(),
  register: vi.fn(),
  reset: vi.fn()
}));

vi.mock("posthog-js", () => ({ default: posthogMock }));

// posthog.ts reads its env at import time, so each test imports a fresh copy.
async function loadModule(key: string) {
  vi.stubEnv("VITE_POSTHOG_KEY", key);
  vi.resetModules();
  return import("./posthog");
}

describe("posthog loading", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.values(posthogMock).forEach((fn) => fn.mockClear());
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllEnvs();
  });

  it("waits for idle, then delivers events captured before it", async () => {
    const { capture, initPostHog } = await loadModule("phc_test");

    initPostHog();
    capture("slice_viewed", { slice_id: "slice_1" });
    await vi.advanceTimersByTimeAsync(1000);

    expect(posthogMock.init).not.toHaveBeenCalled();
    expect(posthogMock.capture).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(600);
    await vi.waitFor(() =>
      expect(posthogMock.capture).toHaveBeenCalledWith("slice_viewed", {
        slice_id: "slice_1"
      })
    );
    expect(posthogMock.init).toHaveBeenCalledTimes(1);
  });

  it("loads nothing without a project key", async () => {
    const { capture, initPostHog } = await loadModule("");

    initPostHog();
    capture("slice_viewed");
    await vi.advanceTimersByTimeAsync(5000);

    expect(posthogMock.init).not.toHaveBeenCalled();
    expect(posthogMock.capture).not.toHaveBeenCalled();
  });
});
