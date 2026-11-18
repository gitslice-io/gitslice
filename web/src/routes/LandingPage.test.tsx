import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    className,
    params,
    to
  }: {
    children: ReactNode;
    className?: string;
    params?: Record<string, string>;
    to: string;
  }) => {
    let href = to;
    for (const [key, value] of Object.entries(params ?? {})) {
      href = href.replace(`$${key}`, value);
    }
    return (
      <a className={className} href={href}>
        {children}
      </a>
    );
  }
}));
vi.mock("../components/ThemeToggle", () => ({ ThemeToggle: () => null }));

import { LandingPage } from "./LandingPage";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("LandingPage", () => {
  it("links to the source and gives a contact email", () => {
    render(<LandingPage />);
    expect(screen.getByRole("link", { name: "gitslice/gitslice" })).toHaveAttribute(
      "href",
      "/slices/gitslice/gitslice"
    );
    expect(screen.queryByRole("link", { name: /github/i })).not.toBeInTheDocument();
    const footer = within(screen.getByRole("navigation", { name: "Footer" }));
    expect(footer.getByRole("link", { name: "Source" })).toHaveAttribute("href", "/slices/gitslice/gitslice");
    expect(footer.getByRole("link", { name: "Blog" })).toHaveAttribute("href", "/blogs");
    expect(footer.getByRole("link", { name: "Docs" })).toHaveAttribute("href", "/doc");
    const header = within(screen.getByRole("navigation", { name: "Landing page" }));
    expect(header.getByRole("link", { name: "Blog" })).toHaveAttribute("href", "/blogs");
    expect(header.getByRole("link", { name: "Docs" })).toHaveAttribute("href", "/doc");
    expect(footer.getByRole("link", { name: "contact@gitslice.io" })).toHaveAttribute(
      "href",
      "mailto:contact@gitslice.io"
    );
  });

  it("leads with landing in parallel, not with slices", () => {
    render(<LandingPage />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Many agents. One codebase. Land in parallel." })
    ).toBeInTheDocument();
    const pillars = within(screen.getByRole("region", { name: /Git was built for people/ }));
    for (const title of ["Land in parallel", "A workspace for the task", "The server decides"]) {
      expect(pillars.getByRole("heading", { name: title })).toBeInTheDocument();
    }
    // Slices are one feature among several, not the headline.
    expect(screen.getByRole("heading", { name: "Slices" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Merge on submit" })).toBeInTheDocument();
  });

  it("shows the measured parallel-landing table, with Git's retries next to Gitslice's", () => {
    render(<LandingPage />);
    const table = within(screen.getByRole("table", { name: /retries/ }));
    const row16 = within(table.getByRole("row", { name: /own file 16/ }));
    expect(row16.getByText("120")).toBeInTheDocument();
    expect(row16.getByText("5.3 s")).toBeInTheDocument();
    expect(row16.getByText("2.4 s")).toBeInTheDocument();
    expect(row16.getByText("0")).toBeInTheDocument();
    // Where Git is faster, the table says so: 8 agents on one file.
    const same8 = within(table.getByRole("row", { name: /One file.* 8 / }));
    expect(same8.getByText("1.4 s")).toBeInTheDocument();
    expect(same8.getByText("1.8 s")).toBeInTheDocument();
    // The numbers are tied to their method, which says where Git is faster.
    expect(screen.getByRole("link", { name: "full benchmarks" })).toHaveAttribute(
      "href",
      "/slices/gitslice/gitslice"
    );
    expect(screen.getByText(/local directory/)).toBeInTheDocument();
  });

  it("does not promise a workspace in a second", () => {
    const { container } = render(<LandingPage />);
    expect(container.textContent).not.toMatch(/in (about )?a second/i);
  });

  it("switches hero workflow tabs with pointer and keyboard navigation", () => {
    render(<LandingPage />);
    const tablist = screen.getByRole("tablist", { name: "Gitslice workflow examples" });
    const cliTab = within(tablist).getByRole("tab", { name: "Agent CLI" });
    const lazyTab = within(tablist).getByRole("tab", { name: "Lazy Mount" });
    const mcpTab = within(tablist).getByRole("tab", { name: "MCP" });

    expect(cliTab).toHaveAttribute("aria-selected", "true");
    expect(lazyTab).toHaveAttribute("aria-selected", "false");
    expect(screen.getByRole("tabpanel", { name: "Agent CLI" })).toBeVisible();

    fireEvent.keyDown(cliTab, { key: "ArrowRight" });
    expect(lazyTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: "Lazy Mount" })).toBeVisible();

    fireEvent.click(mcpTab);
    expect(mcpTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: "MCP" })).toBeVisible();
  });

  it("copies the install command and agent prompt via accessible copy buttons", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText }
    });

    render(<LandingPage />);
    const installBtn = screen.getByRole("button", { name: "Copy install command" });
    fireEvent.click(installBtn);

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith("curl -fsSL https://gitslice.io/install.sh | sh");
    });

    const promptBtn = screen.getByRole("button", { name: "Copy agent prompt" });
    fireEvent.click(promptBtn);

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        "Read https://gitslice.io/llms.txt, install gs, and register yourself under my email."
      );
    });
  });
});
