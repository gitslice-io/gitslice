import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ActionMenu, type ActionMenuItem } from "./ActionMenu";

function renderMenu(items: ActionMenuItem[]) {
  render(<ActionMenu items={items} label="File actions" />);
  return screen.getByRole("button", { name: "File actions" });
}

describe("ActionMenu", () => {
  afterEach(() => {
    cleanup();
  });

  it("opens from the keyboard and focuses the first enabled item", () => {
    const trigger = renderMenu([
      { label: "Edit", onSelect: vi.fn() },
      { label: "Rename", onSelect: vi.fn() }
    ]);

    fireEvent.keyDown(trigger, { key: "ArrowDown" });

    const menu = screen.getByRole("menu", { name: "File actions" });
    expect(trigger).toHaveAttribute("aria-controls", menu.id);
    expect(screen.getByRole("menuitem", { name: "Edit" })).toHaveFocus();
  });

  it("moves with the arrow keys, skipping disabled items and wrapping", () => {
    const trigger = renderMenu([
      { label: "Edit", onSelect: vi.fn() },
      { disabled: true, label: "Rename", onSelect: vi.fn() },
      { label: "Delete", onSelect: vi.fn(), tone: "danger" }
    ]);
    fireEvent.click(trigger);
    const menu = screen.getByRole("menu");

    fireEvent.keyDown(menu, { key: "ArrowDown" });
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();

    fireEvent.keyDown(menu, { key: "ArrowDown" });
    expect(screen.getByRole("menuitem", { name: "Edit" })).toHaveFocus();

    fireEvent.keyDown(menu, { key: "End" });
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
  });

  it("closes on Escape and returns focus to the trigger", () => {
    const trigger = renderMenu([{ label: "Edit", onSelect: vi.fn() }]);
    fireEvent.click(trigger);

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("closes on Tab with focus back on the trigger", () => {
    const trigger = renderMenu([{ label: "Edit", onSelect: vi.fn() }]);
    fireEvent.click(trigger);

    fireEvent.keyDown(screen.getByRole("menu"), { key: "Tab" });

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("refocuses the trigger before running the selected item", () => {
    let focusedDuringSelect: Element | null = null;
    const trigger = renderMenu([
      {
        label: "Delete",
        onSelect: () => {
          focusedDuringSelect = document.activeElement;
        }
      }
    ]);
    fireEvent.click(trigger);

    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));

    expect(focusedDuringSelect).toBe(trigger);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
