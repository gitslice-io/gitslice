import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PendingClaims } from "./PendingClaims";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

const authMock = vi.hoisted(() => ({
  current: { isLoaded: true, isSignedIn: true }
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => authMock.current
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>
}));

function renderClaims() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <PendingClaims />
    </QueryClientProvider>
  );
}

describe("PendingClaims", () => {
  afterEach(() => {
    cleanup();
    authMock.current = { isLoaded: true, isSignedIn: true };
  });

  it("renders nothing when there is nothing to claim", async () => {
    const listPendingClaims = vi.fn().mockResolvedValue({ claims: [] });
    apiMock.current = { listPendingClaims };
    const { container } = renderClaims();
    await waitFor(() => expect(listPendingClaims).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when signed out", () => {
    authMock.current = { isLoaded: true, isSignedIn: false };
    const listPendingClaims = vi.fn();
    apiMock.current = { listPendingClaims };
    const { container } = renderClaims();
    expect(container).toBeEmptyDOMElement();
    expect(listPendingClaims).not.toHaveBeenCalled();
  });

  it("accepts a claim", async () => {
    const listPendingClaims = vi
      .fn()
      .mockResolvedValueOnce({
        claims: [
          {
            agentSubjectId: "agent_1",
            agentDisplayName: "Release bot",
            account: "release-bot",
            ownerEmail: "me@example.com",
            createdAt: "2026-09-30T00:00:00Z"
          }
        ]
      })
      .mockResolvedValue({ claims: [] });
    const acceptClaim = vi.fn().mockResolvedValue({ account: "release-bot" });
    apiMock.current = { listPendingClaims, acceptClaim };

    renderClaims();
    expect(await screen.findByText("An agent account is waiting for you")).toBeInTheDocument();
    expect(screen.getByText("Release bot")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(acceptClaim).toHaveBeenCalledWith({ agentSubjectId: "agent_1" }));
    expect(await screen.findByRole("status")).toHaveTextContent("You now co-own release-bot.");
  });
});
