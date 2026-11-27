import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Alert } from "./Alert";
import { PortalMenu } from "./PortalMenu";

afterEach(cleanup);

describe("PortalMenu", () => {
  it("opens in a portal, closes on Escape, and closes on outside click", () => {
    render(
      <div>
        <button type="button">Outside</button>
        <PortalMenu
          renderTrigger={({ open, ref, toggle }) => (
            <button aria-expanded={open} onClick={toggle} ref={ref} type="button">
              Trigger
            </button>
          )}
        >
          {({ close }) => (
            <button onClick={close} role="menuitem" type="button">
              Action
            </button>
          )}
        </PortalMenu>
      </div>
    );

    const trigger = screen.getByRole("button", { name: "Trigger" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();

    fireEvent.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();

    fireEvent.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByRole("button", { name: "Outside" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});

describe("Alert", () => {
  it("sets role=alert for error tone and role=status for success tone", () => {
    const { rerender } = render(
      <Alert title="Failed" tone="error">
        Something went wrong
      </Alert>
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Failed");

    rerender(
      <Alert title="Saved" tone="success">
        Changes persisted
      </Alert>
    );
    expect(screen.getByRole("status")).toHaveTextContent("Saved");
  });
});
