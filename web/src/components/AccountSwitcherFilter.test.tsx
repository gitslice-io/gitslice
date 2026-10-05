import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useNavigate: () => vi.fn()
}));

const agents = Array.from({ length: 9 }, (_, i) => ({ account: `agent-${i}`, kind: "agent", role: "owner" }));

vi.mock("../state/selection", () => ({
  useSelection: () => ({
    activeAccount: "nic",
    activeMembership: { account: "nic", kind: "personal", role: "owner" },
    memberships: [{ account: "nic", kind: "personal", role: "owner" }, { account: "acme", kind: "organization", role: "admin" }, ...agents],
    setActiveAccount: vi.fn()
  })
}));

vi.mock("./slices/PendingInvitations", () => ({ useMyInvitations: () => ({ data: [] }) }));

import { AccountSwitcher } from "./AccountSwitcher";

afterEach(cleanup);

describe("AccountSwitcher with many accounts", () => {
  it("renders above the page, outside the top bar, and filters", () => {
    const { container } = render(<AccountSwitcher />);
    fireEvent.click(screen.getByRole("button", { name: /Switch account/ }));

    const menu = screen.getByRole("menu");
    // In a portal on the body, so no header's stacking context can cover it.
    expect(container.contains(menu)).toBe(false);
    expect(menu).toHaveClass("fixed");
    expect(screen.getAllByRole("menuitemradio")).toHaveLength(11);

    fireEvent.change(screen.getByLabelText("Find an account"), { target: { value: "acm" } });
    expect(screen.getAllByRole("menuitemradio")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Find an account"), { target: { value: "zzz" } });
    expect(screen.getByText("No account matches.")).toBeInTheDocument();
  });

  it("stays open while the menu itself is clicked", () => {
    render(<AccountSwitcher />);
    fireEvent.click(screen.getByRole("button", { name: /Switch account/ }));
    fireEvent.pointerDown(screen.getByLabelText("Find an account"));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
