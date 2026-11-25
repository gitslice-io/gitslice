import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    className,
    to
  }: {
    children: ReactNode;
    className?: string;
    to: string;
  }) => (
    <a className={className} href={to}>
      {children}
    </a>
  )
}));

import { AppFooter } from "./AppFooter";

afterEach(cleanup);

describe("AppFooter", () => {
  it("renders institutional trust, status, releases, and legal links for signed-in and public app pages", () => {
    render(<AppFooter />);
    const nav = within(screen.getByRole("navigation", { name: "Footer" }));
    expect(nav.getByRole("link", { name: "Docs" })).toHaveAttribute("href", "/doc");
    expect(nav.getByRole("link", { name: "CLI Releases" })).toHaveAttribute("href", "/releases");
    expect(nav.getByRole("link", { name: "Changelog" })).toHaveAttribute("href", "/changelog");
    expect(nav.getByRole("link", { name: "Status" })).toHaveAttribute("href", "/status");
    expect(nav.getByRole("link", { name: "Security" })).toHaveAttribute("href", "/security");
    expect(nav.getByRole("link", { name: "Pricing" })).toHaveAttribute("href", "/pricing");
    expect(nav.getByRole("link", { name: "About" })).toHaveAttribute("href", "/about");
    expect(nav.getByRole("link", { name: "Privacy" })).toHaveAttribute("href", "/privacy");
    expect(nav.getByRole("link", { name: "Terms" })).toHaveAttribute("href", "/terms");
    expect(nav.getByRole("link", { name: "Contact" })).toHaveAttribute("href", "mailto:contact@gitslice.io");
  });
});
