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
  it("keeps the trigger compact on phones while preserving the accessible label and menu details", () => {
    render(
      <AppAuthContext.Provider value={makeAgentAuth()}>
        <AgentAccountMenu />
      </AppAuthContext.Provider>
    );

    const trigger = screen.getByRole("button", { name: "Agent session: antigravity-nic" });
    expect(trigger).toHaveClass("size-8", "sm:w-auto");
    expect(screen.getByText("antigravity-nic")).toHaveClass("sr-only", "sm:not-sr-only");

    fireEvent.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(screen.getByText("@antigravity-nic")).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Sign out & revoke session" })).toBeInTheDocument();
  });
});
