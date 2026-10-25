import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CopyRow } from "./CopyRow";

afterEach(cleanup);

describe("CopyRow", () => {
  it("names the command and its copy button, and copies the command", () => {
    const writeText = vi.fn();
    Object.assign(navigator, { clipboard: { writeText } });
    render(<CopyRow label="git clone command" value="git clone https://x.test/a.git" />);

    const input = screen.getByRole("textbox", { name: "git clone command" });
    expect(input).toHaveValue("git clone https://x.test/a.git");
    expect(input).toHaveAttribute("readonly");
    fireEvent.click(screen.getByRole("button", { name: "Copy git clone command" }));
    expect(writeText).toHaveBeenCalledWith("git clone https://x.test/a.git");
  });
});
