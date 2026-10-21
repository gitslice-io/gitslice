import "@testing-library/jest-dom/vitest";

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { applyTheme } from "../../theme";
import { MarkdownViewer } from "./MarkdownViewer";

afterEach(() => {
  cleanup();
  applyTheme("light");
});

const readme = `<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="Imagens/Logo%20Dark.png">
    <img src="Imagens/Logo%20Light.png" alt="Logo" width="360">
  </picture>
</div>
`;

describe("MarkdownViewer <picture>", () => {
  it("shows the logo for the site's theme and switches when the theme does", async () => {
    applyTheme("light");
    const loadImage = vi.fn(async (path: string) => `data:image/png;base64,${btoa(path)}`);
    render(<MarkdownViewer links={{ directory: "/sunny/app", loadImage, onOpenPath: vi.fn() }} source={readme} />);

    await waitFor(() =>
      expect(screen.getByRole("img", { name: "Logo" })).toHaveAttribute(
        "src",
        `data:image/png;base64,${btoa("/sunny/app/Imagens/Logo Light.png")}`
      ),
      // The first render loads the Markdown renderer, slow on a busy host.
      { timeout: 5000 }
    );
    expect(document.querySelector("source")).toBeNull();

    act(() => applyTheme("dark"));
    await waitFor(() =>
      expect(screen.getByRole("img", { name: "Logo" })).toHaveAttribute(
        "src",
        `data:image/png;base64,${btoa("/sunny/app/Imagens/Logo Dark.png")}`
      )
    );
    expect(loadImage).toHaveBeenCalledWith("/sunny/app/Imagens/Logo Dark.png");
  });
});
