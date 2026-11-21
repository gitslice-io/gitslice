// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
  signInWithAgentKey: vi.fn(),
  isLoaded: true,
  isSignedIn: false
}));

vi.mock("../auth/AppAuthProvider", () => ({
  useAppAuth: () => ({
    agentAccount: null,
    agentExpiresAt: null,
    agentKeyId: null,
    authKind: authState.isSignedIn ? "agent" : null,
    getToken: async () => null,
    isLoaded: authState.isLoaded,
    isSignedIn: authState.isSignedIn,
    signInWithAgentKey: authState.signInWithAgentKey,
    signOut: async () => undefined,
    userId: null
  })
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a>,
  Navigate: ({ to }: { to: string }) => <div>redirect to {to}</div>
}));
vi.mock("../components/AuthFrame", () => ({
  AuthFrame: ({ children, title }: { children: ReactNode; title: string }) => (
    <section>
      <h1>{title}</h1>
      {children}
    </section>
  )
}));

import { AgentLoginPage } from "./AgentLoginPage";

afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "/agent-login");
  vi.clearAllMocks();
});

describe("AgentLoginPage", () => {
  it("renders the API key form when opened without a fragment key", () => {
    authState.isSignedIn = false;
    window.history.replaceState(null, "", "/agent-login");
    render(<AgentLoginPage />);
    expect(screen.getByRole("heading", { name: "Agent sign in" })).toBeInTheDocument();
    expect(screen.getByLabelText("Agent API key")).toBeInTheDocument();
  });

  it("reads #key from the URL fragment, strips it from history, and signs in", async () => {
    authState.isSignedIn = false;
    authState.signInWithAgentKey.mockRejectedValueOnce(new Error("API key was rejected"));
    window.history.replaceState(null, "", "/agent-login?returnTo=%2Fslices%2Facme%2Fapp#key=gsk_test_123");

    render(<AgentLoginPage />);

    await waitFor(() => {
      expect(authState.signInWithAgentKey).toHaveBeenCalledWith("gsk_test_123");
    });
    expect(window.location.hash).toBe("");
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("API key was rejected");
    });
  });
});
