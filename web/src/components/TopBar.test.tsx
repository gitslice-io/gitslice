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

// The theme toggle is not under test here.
vi.mock("./ThemeToggle", () => ({ ThemeToggle: () => null }));

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
});
