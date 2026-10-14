import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, within } from "@testing-library/react";
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

afterEach(cleanup);

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
    expect(footer.getByRole("link", { name: "contact@gitslice.io" })).toHaveAttribute(
      "href",
      "mailto:contact@gitslice.io"
    );
  });
});
