import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MarkdownViewer } from "./MarkdownViewer";
import { resolvePictures } from "../../lib/repoLinks";

afterEach(() => {
  cleanup();
  document.documentElement.classList.remove("dark");
});

const serverHtml = '<h1 align="center">Hello</h1><p>Read the <a href="docs/guide.md">guide</a>.</p>';

describe("MarkdownViewer with HTML the server rendered", () => {
  it("is in the server's render of the page, not left for the browser", () => {
    const html = renderToString(<MarkdownViewer html={serverHtml} />);
    expect(html).toContain('<h1 align="center">Hello</h1>');
    expect(html).toContain("guide</a>");
  });

  it("shows it on the first render in the browser too, so hydration matches", () => {
    render(<MarkdownViewer html={serverHtml} />);
    // No waiting: the same markup as the server's, at once.
    expect(screen.getByRole("heading", { name: "Hello" })).toBeInTheDocument();
  });

  it("opens a relative link in the slice", async () => {
    const onOpenPath = vi.fn();
    render(<MarkdownViewer html={serverHtml} links={{ directory: "/acme/app", onOpenPath }} />);
    screen.getByRole("link", { name: "guide" }).click();
    expect(onOpenPath).toHaveBeenCalledWith("/acme/app/docs/guide.md");
  });

  it("loads a repository image the server marked, through the loader", async () => {
    const loadImage = vi.fn().mockResolvedValue("data:image/png;base64,eA==");
    render(
      <MarkdownViewer
        html='<p><img alt="logo" data-repo-image="/acme/app/logo.png"></p>'
        links={{ directory: "/acme/app", loadImage, onOpenPath: vi.fn() }}
      />
    );
    await waitFor(() => expect(screen.getByRole("img", { name: "logo" })).toHaveAttribute("src", "data:image/png;base64,eA=="));
    expect(loadImage).toHaveBeenCalledWith("/acme/app/logo.png");
  });
});

describe("resolvePictures with server-rendered sources", () => {
  const picture =
    '<picture><source media="(prefers-color-scheme: dark)" data-repo-srcset="/acme/app/logo-dark.png"><img alt="Logo" data-repo-image="/acme/app/logo.png"></picture>';

  it("loads the dark source's repository image on a dark page", () => {
    const html = resolvePictures(picture, true);
    expect(html).not.toContain("<picture");
    expect(html).toContain('data-repo-image="/acme/app/logo-dark.png"');
  });

  it("keeps the image's own on a light page", () => {
    const html = resolvePictures(picture, false);
    expect(html).toContain('data-repo-image="/acme/app/logo.png"');
    expect(html).not.toContain("logo-dark");
  });
});
