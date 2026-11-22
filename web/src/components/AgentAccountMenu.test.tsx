import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AppAuthContext, type AppAuthState } from "../auth/AppAuthProvider";
import { AgentAccountMenu } from "./AgentAccountMenu";

const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...rest }: { children: ReactNode } & Record<string, unknown>) => (
    <a {...rest} href="#">{children}</a>
  ),
  useNavigate: () => navigate
}));

vi.mock("../state/selection", () => ({
  useSelection: () => ({ account: "antigravity-nic" })
}));

function makeAgentAuth(overrides: Partial<AppAuthState> = {}): AppAuthState {
  return {
    agentAccount: "antigravity-nic",
    agentExpiresAt: "2026-11-22T01:31:59Z",
    agentKeyId: "key_1",
    authKind: "agent",
    getToken: async () => "tok",
    isLoaded: true,
    isSignedIn: true,
    signInWithAgentKey: vi.fn(),
    signOut: vi.fn().mockResolvedValue(undefined),
    userId: "agent_123",
    ...overrides
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("AgentAccountMenu", () => {
  it("keeps the trigger compact without duplicating the account slug and renders the menu in a portal", () => {
    render(
      <AppAuthContext.Provider value={makeAgentAuth()}>
        <AgentAccountMenu />
      </AppAuthContext.Provider>
    );

    const trigger = screen.getByRole("button", { name: "Agent session: antigravity-nic" });
    expect(trigger).toHaveClass("size-8", "sm:w-auto");
    expect(trigger).toHaveAttribute("title", "Signed in as agent @antigravity-nic");
    expect(screen.queryByText("@antigravity-nic")).not.toBeInTheDocument();

    fireEvent.click(trigger);
    const menu = screen.getByRole("menu");
    expect(menu).toBeInTheDocument();
    expect(menu.parentElement).toBe(document.body);
    expect(screen.getByText("@antigravity-nic")).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Sign out & revoke session" })).toBeInTheDocument();
  });
});
