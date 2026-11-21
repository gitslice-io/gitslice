import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ensureCopyCommandListener } from "./copyCommand";

describe("copyCommand", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("handles copy clicks immediately on import without waiting for useEffect, and does not double-register", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText }
    });

    // Calling ensureCopyCommandListener again must be idempotent.
    ensureCopyCommandListener();

    const attrs = { command: "--copy", commandfor: "cmd-target" } as Record<string, string>;
    render(
      <div>
        <button aria-label="Copy cmd" type="button" {...attrs}>
          Copy
        </button>
        <span role="status" />
        <code id="cmd-target">gs status</code>
      </div>
    );

    const button = screen.getByRole("button", { name: "Copy cmd" });
    fireEvent.click(button);

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledTimes(1);
      expect(writeText).toHaveBeenCalledWith("gs status");
    });
    await waitFor(() => {
      expect(button).toHaveAttribute("data-copied", "");
    });
  });
});
