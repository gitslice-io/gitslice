import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AccountAvatar, AccountLink, AuthorLabel, avatarUrl, rememberAvatarVersion } from "./AccountAvatar";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, params, to, ...rest }: { children: ReactNode; params?: { account?: string }; to: string }) => (
    <a {...(rest as Record<string, unknown>)} href={to.replace("$account", params?.account ?? "")}>
      {children}
    </a>
  )
}));

afterEach(cleanup);

describe("AccountAvatar", () => {
  it("addresses a picture by account and version", () => {
    expect(avatarUrl("anna")).toBe("/avatars/anna.png");
    expect(avatarUrl("anna", "")).toBe("/avatars/anna.png?v=none");
    expect(avatarUrl("anna", "0123abcd")).toBe("/avatars/anna.png?v=0123abcd");
  });

  it("shows the account's picture, square for organizations", () => {
    const { container } = render(<AccountAvatar account="labs" kind="organization" version="" />);
    const img = container.querySelector("img");
    expect(img).toHaveAttribute("src", "/avatars/labs.png?v=none");
    expect(img).toHaveClass("rounded-md");
  });

  it("follows the version remembered for the account", () => {
    const { container } = render(<AccountAvatar account="bea" kind="personal" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "/avatars/bea.png");
    act(() => rememberAvatarVersion("bea", "feed"));
    expect(container.querySelector("img")).toHaveAttribute("src", "/avatars/bea.png?v=feed");
  });

  it("falls back to the first letter when the picture does not load", () => {
    const { container } = render(<AccountAvatar account="zed" kind="agent" version="" />);
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(container).toHaveTextContent("z");
  });

  it("puts an author's picture before their name, linked to their profile, but neither for a raw subject id", () => {
    const { container, rerender } = render(<AuthorLabel name="sunny" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "/avatars/sunny.png");
    expect(container).toHaveTextContent("sunny");
    // The picture and the name are one link.
    const link = screen.getByRole("link", { name: "sunny" });
    expect(link).toHaveAttribute("href", "/accounts/sunny");
    expect(link.querySelector("img")).not.toBeNull();

    rerender(<AuthorLabel name="user_ext_7cac3e86bbbe5541" />);
    expect(container.querySelector("img")).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();
    expect(container).toHaveTextContent("user_ext_7cac3e86bbbe5541");
  });

  it("links an account name to its profile without triggering the row it sits in", () => {
    let rowClicks = 0;
    let navigated = 0;
    render(
      <div onClick={() => (rowClicks += 1)}>
        <AccountLink account="bea" onNavigate={() => (navigated += 1)} />
      </div>
    );
    const link = screen.getByRole("link", { name: "bea" });
    expect(link).toHaveAttribute("href", "/accounts/bea");
    expect(link).toHaveAttribute("title", "View @bea's profile");
    fireEvent.click(link);
    expect(navigated).toBe(1);
    expect(rowClicks).toBe(0);
  });
});
