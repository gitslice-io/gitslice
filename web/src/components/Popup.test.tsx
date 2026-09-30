import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { Popup } from "./Popup";

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)} type="button">
        Open settings
      </button>
      <Popup onClose={() => setOpen(false)} open={open} title="Settings">
        <p>Body</p>
      </Popup>
    </>
  );
}

describe("Popup", () => {
  afterEach(() => {
    cleanup();
  });

  it("is a labelled modal dialog that focuses its close button", () => {
    render(<Harness />);
    const opener = screen.getByRole("button", { name: "Open settings" });
    opener.focus();

    fireEvent.click(opener);

    expect(screen.getByRole("dialog", { name: "Settings" })).toHaveAttribute(
      "aria-modal",
      "true"
    );
    const closeButtons = screen.getAllByRole("button", { name: "Close Settings" });
    // The backdrop closes on click but stays out of the tab order.
    expect(closeButtons.filter((button) => button.tabIndex >= 0)).toHaveLength(1);
    expect(closeButtons.find((button) => button.tabIndex >= 0)).toHaveFocus();
  });

  it("returns focus to the opener when it closes", () => {
    render(<Harness />);
    const opener = screen.getByRole("button", { name: "Open settings" });
    opener.focus();
    fireEvent.click(opener);

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });
});
