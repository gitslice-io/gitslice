import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Badge } from "./Badge";

describe("Badge", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders neutral, tertiary, and primary tones", () => {
    render(
      <div>
        <Badge>Neutral</Badge>
        <Badge variant="tertiary">Gold</Badge>
        <Badge variant="primary">Primary</Badge>
      </div>
    );

    expect(screen.getByText("Neutral").className).toContain(
      "bg-surface-container-high"
    );
    expect(screen.getByText("Gold").className).toContain("bg-tertiary-container");
    expect(screen.getByText("Primary").className).toContain("bg-primary/10");
  });
});
