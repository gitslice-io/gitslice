import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Button } from "./Button";

describe("Button", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders primary, secondary, and tertiary variants", () => {
    render(
      <div>
        <Button>Primary</Button>
        <Button variant="secondary">Secondary</Button>
        <Button variant="tertiary">Tertiary</Button>
      </div>
    );

    expect(screen.getByRole("button", { name: "Primary" }).className).toContain(
      "from-primary"
    );
    expect(screen.getByRole("button", { name: "Secondary" }).className).toContain(
      "bg-surface-container-high"
    );
    expect(screen.getByRole("button", { name: "Tertiary" }).className).toContain(
      "hover:underline"
    );
  });

  it("defaults to type button", () => {
    render(<Button>Save</Button>);

    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty(
      "type",
      "button"
    );
  });
});
