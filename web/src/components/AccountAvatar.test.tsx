import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { AccountAvatar, avatarUrl, rememberAvatarVersion } from "./AccountAvatar";

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
});
