import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const navigate = vi.fn();
const setActiveAccount = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, onClick }: { children: ReactNode; onClick?: () => void }) => (
    <a href="#" onClick={onClick}>
      {children}
    </a>
  ),
  useNavigate: () => navigate
}));

vi.mock("../state/selection", () => ({
  useSelection: () => ({
    activeAccount: "nic",
    activeMembership: { account: "nic", kind: "personal", role: "owner" },
    memberships: [
      { account: "nic", kind: "personal", role: "owner" },
      { account: "gitslice", kind: "organization", role: "owner" },
      { account: "acme", kind: "organization", role: "reader" },
      { account: "heibot", kind: "agent", role: "owner" }
    ],
    setActiveAccount
  })
}));

import { AccountSwitcher } from "./AccountSwitcher";

afterEach(cleanup);

describe("AccountSwitcher", () => {
  it("lists personal, organization and agent accounts and switches", () => {
    render(<AccountSwitcher />);

    fireEvent.click(screen.getByRole("button", { name: "Switch account (current: nic)" }));
    expect(screen.getByText("Organizations")).toBeInTheDocument();
    expect(screen.getByText("Agents")).toBeInTheDocument();
    expect(screen.getByRole("menuitemradio", { name: /nic/ })).toHaveAttribute("aria-checked", "true");

    fireEvent.click(screen.getByRole("menuitemradio", { name: /gitslice/ }));
    expect(setActiveAccount).toHaveBeenCalledWith("gitslice");
    expect(navigate).toHaveBeenCalledWith({ to: "/" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("closes on Escape", () => {
    render(<AccountSwitcher />);

    fireEvent.click(screen.getByRole("button", { name: /Switch account/ }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
