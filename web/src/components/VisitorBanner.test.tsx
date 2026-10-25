import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const auth = vi.hoisted(() => ({ current: { isLoaded: true, isSignedIn: false } }));
const router = vi.hoisted(() => ({ pathname: "/slices/gitslice/gitslice" }));

vi.mock("@clerk/tanstack-react-start", () => ({ useAuth: () => auth.current }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...rest }: { children: ReactNode; to: string }) => (
    <a {...(rest as Record<string, unknown>)} href={to}>
      {children}
    </a>
  ),
  useRouterState: ({ select }: { select(state: unknown): unknown }) => select({ location: { pathname: router.pathname } })
}));

import { VisitorBanner } from "./VisitorBanner";

beforeEach(() => {
  auth.current = { isLoaded: true, isSignedIn: false };
  router.pathname = "/slices/gitslice/gitslice";
  document.cookie = "gitslice_welcome=; Max-Age=0; Path=/";
});
afterEach(cleanup);

describe("VisitorBanner", () => {
  it("says what Gitslice is and offers to sign up or learn more", () => {
    render(<VisitorBanner show />);
    const banner = screen.getByRole("region", { name: "About Gitslice" });
    expect(banner).toHaveTextContent("source control for fleets of agents and the people who work with them");
    expect(screen.getByRole("link", { name: "Sign up" })).toHaveAttribute("href", "/signup");
    expect(screen.getByRole("link", { name: "Learn more" })).toHaveAttribute("href", "/");
  });

  it("is not there when the route does not want it", () => {
    render(<VisitorBanner show={false} />);
    expect(screen.queryByRole("region", { name: "About Gitslice" })).toBeNull();
  });

  it("goes once someone is signed in, even if the server could not tell", () => {
    auth.current = { isLoaded: true, isSignedIn: true };
    render(<VisitorBanner show />);
    expect(screen.queryByRole("region", { name: "About Gitslice" })).toBeNull();
    // While Clerk has not loaded, it stays: nobody is known to be signed in.
    cleanup();
    auth.current = { isLoaded: false, isSignedIn: false };
    render(<VisitorBanner show />);
    expect(screen.getByRole("region", { name: "About Gitslice" })).toBeInTheDocument();
  });

  it("is left off the docs and the blog", () => {
    for (const pathname of ["/doc", "/doc/cli", "/blogs/a-post"]) {
      router.pathname = pathname;
      render(<VisitorBanner show />);
      expect(screen.queryByRole("region", { name: "About Gitslice" })).toBeNull();
      cleanup();
    }
  });

  it("is dismissed for good: it goes now and a cookie tells the server next time", () => {
    render(<VisitorBanner show />);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("region", { name: "About Gitslice" })).toBeNull();
    expect(document.cookie).toContain("gitslice_welcome=1");
  });
});
