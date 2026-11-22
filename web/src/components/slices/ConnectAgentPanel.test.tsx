import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ConnectAgentPanel } from "./ConnectAgentPanel";

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

function renderPanel(slice?: { account: string; slice: string }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <ConnectAgentPanel slice={slice} />
    </QueryClientProvider>
  );
}

describe("ConnectAgentPanel", () => {
  afterEach(() => {
    cleanup();
    authMock.current = { isLoaded: true, isSignedIn: true };
  });

  it("creates a single-use invite and switches between prompt, CLI, and MCP tabs", async () => {
    const listAgentInvites = vi.fn().mockResolvedValue({ invites: [] });
    const createAgentInvite = vi.fn().mockResolvedValue({
      token: "gsi_testtoken123",
      invite: {
        id: "inv_1",
        slice: { account: "acme", slice: "api" },
        role: "writer",
        suggestedUsername: "triage-bot",
        expiresAt: "2026-12-01T00:00:00Z"
      }
    });
    apiMock.current = { listAgentInvites, createAgentInvite };

    renderPanel({ account: "acme", slice: "api" });

    fireEvent.change(screen.getByLabelText("Slice role"), { target: { value: "writer" } });
    fireEvent.change(screen.getByLabelText("Suggested username"), {
      target: { value: "triage-bot" }
    });
    fireEvent.click(screen.getByRole("button", { name: "Generate join token" }));

    await waitFor(() =>
      expect(createAgentInvite).toHaveBeenCalledWith({
        slice: { account: "acme", slice: "api" },
        role: "writer",
        suggestedUsername: "triage-bot",
        expiresInSeconds: "86400"
      })
    );

    expect(await screen.findByText(/gsi_testtoken123/)).toBeInTheDocument();
    expect(screen.getByText(/gitslice\.io\/SKILL\.md/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Hosted MCP" }));
    expect(screen.getByText(/https:\/\/gitslice\.io\/mcp\?slice=acme%2Fapi/)).toBeInTheDocument();
  });

  it("lists active invites and revokes an invite", async () => {
    const listAgentInvites = vi
      .fn()
      .mockResolvedValueOnce({
        invites: [
          {
            id: "inv_active",
            slice: { account: "acme", slice: "api" },
            role: "contributor",
            expiresAt: "2026-12-01T00:00:00Z"
          }
        ]
      })
      .mockResolvedValue({ invites: [] });
    const revokeAgentInvite = vi.fn().mockResolvedValue({});
    apiMock.current = { listAgentInvites, revokeAgentInvite };

    renderPanel({ account: "acme", slice: "api" });

    expect(await screen.findByText(/inv_active/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(revokeAgentInvite).toHaveBeenCalledWith({ inviteId: "inv_active" }));
  });
});
