import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CopyButton } from "./CopyButton";

const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
const execCommandDescriptor = Object.getOwnPropertyDescriptor(document, "execCommand");

function restoreProperty(
  target: Navigator | Document,
  property: "clipboard" | "execCommand",
  descriptor: PropertyDescriptor | undefined
) {
  if (descriptor) {
    Object.defineProperty(target, property, descriptor);
    return;
  }
  delete (target as Navigator & Document & Record<string, unknown>)[property];
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  restoreProperty(navigator, "clipboard", clipboardDescriptor);
  restoreProperty(document, "execCommand", execCommandDescriptor);
});

describe("CopyButton", () => {
  it("copies text and confirms the action", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText }
    });

    render(<CopyButton text="gs status" />);

    fireEvent.click(screen.getByRole("button", { name: "Copy to clipboard" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("gs status"));
    expect(screen.getByRole("status")).toHaveTextContent("Copied");
  });

  it("uses the textarea fallback when the Clipboard API is unavailable", async () => {
    const execCommand = vi.fn().mockReturnValue(true);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined
    });
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      value: execCommand
    });

    render(<CopyButton text="gs diff" />);

    fireEvent.click(screen.getByRole("button", { name: "Copy to clipboard" }));

    await waitFor(() => expect(execCommand).toHaveBeenCalledWith("copy"));
    expect(screen.getByRole("status")).toHaveTextContent("Copied");
  });
});
