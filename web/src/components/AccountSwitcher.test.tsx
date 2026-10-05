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

vi.mock("./slices/PendingInvitations", () => ({
  useMyInvitations: () => ({ data: [{ account: "labs", role: "writer", invitedBy: "boss" }] })
}));

import { AccountSwitcher, menuPlace } from "./AccountSwitcher";

afterEach(cleanup);

describe("AccountSwitcher", () => {
  it("lists personal, organization and agent accounts and switches", () => {
    render(<AccountSwitcher />);

    expect(screen.getByLabelText("1 pending invitation")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Switch account \(current: nic\)/ }));
    expect(screen.getByText("Organizations")).toBeInTheDocument();
    // (The router's Link is mocked as a plain link here.)
    expect(screen.getByRole("link", { name: "New organization" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "1 pending invitation" })).toBeInTheDocument();
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

describe("menuPlace", () => {
  it("stays inside a phone's screen", () => {
    // A 390px-wide phone with the button near the left: the menu cannot be
    // right-aligned with it, so it is pushed right, and is as wide as fits.
    const place = menuPlace({ bottom: 60, right: 200 }, 390, 800);
    expect(place.left).toBeGreaterThanOrEqual(8);
    expect(place.left + place.width).toBeLessThanOrEqual(390 - 8);
    expect(place.top).toBe(64);
    expect(place.maxHeight).toBe(800 - 64 - 8);
  });

  it("right-aligns with the button on a wide screen", () => {
    const place = menuPlace({ bottom: 50, right: 1200 }, 1440, 900);
    expect(place.width).toBe(288);
    expect(place.left).toBe(1200 - 288);
  });

  it("narrows on a very small screen", () => {
    expect(menuPlace({ bottom: 50, right: 300 }, 280, 600).width).toBe(280 - 16);
  });
});
