import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const auth = vi.hoisted(() => ({ current: { isLoaded: true, isSignedIn: false } }));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => auth.current,
  UserButton: () => <span>user</span>
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useRouterState: () => "/"
}));

vi.mock("../state/selection", () => ({ useSelection: () => ({ account: "" }) }));

// The theme toggle and claims notice are not under test here.
vi.mock("./ThemeToggle", () => ({ ThemeToggle: () => null }));
vi.mock("./ClaimsNotice", () => ({ ClaimsNotice: () => null }));

import { TopBar } from "./TopBar";

afterEach(cleanup);

describe("TopBar", () => {
  it("links the blog for visitors", () => {
    auth.current = { isLoaded: true, isSignedIn: false };
    render(<TopBar />);
    expect(screen.getByRole("link", { name: "Blog" })).toBeInTheDocument();
  });

  it("drops the blog once signed in", () => {
    auth.current = { isLoaded: true, isSignedIn: true };
    render(<TopBar />);
    expect(screen.queryByRole("link", { name: "Blog" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Doc" })).toBeInTheDocument();
  });

  it("has no Conversations entry while conversations are not offered", () => {
    for (const signedIn of [false, true]) {
      auth.current = { isLoaded: true, isSignedIn: signedIn };
      render(<TopBar />);
      expect(screen.queryByRole("link", { name: /Conversations/ })).not.toBeInTheDocument();
      expect(screen.getByRole("link", { name: /Home/ })).toBeInTheDocument();
      cleanup();
    }
  });

  it("shows the name of the page you are on beside its icon, and leaves the others to screen readers on a phone", () => {
    auth.current = { isLoaded: true, isSignedIn: false };
    render(<TopBar />);
    // On "/", Home is the current page: its label is visible at every width.
    expect(screen.getByText("Home")).not.toHaveClass("sr-only");
    // The others show only their icon below sm, but are still named.
    expect(screen.getByText("Doc")).toHaveClass("sr-only", "sm:not-sr-only");
    expect(screen.getByText("Blog")).toHaveClass("sr-only", "sm:not-sr-only");
  });
});
