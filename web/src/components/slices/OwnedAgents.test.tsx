import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { OwnedAgents } from "./OwnedAgents";
import { SlicesList } from "./SlicesList";

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
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useSearch: () => ({})
}));

vi.mock("../../state/selection", () => ({
  useSelection: () => ({
    account: "nic",
    accounts: ["nic", "heibot"],
    error: null,
    isLoading: false,
    needsUsername: false,
    subjectId: "user_1"
  })
}));

function renderWithQuery(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
  );
}

const heibot = {
  agentSubjectId: "agent_1",
  agentDisplayName: "hei",
  account: "heibot",
  registeredAt: "2026-09-30T01:53:48Z",
  claimedAt: "2026-09-30T02:01:48Z",
  lastActiveAt: ""
};

describe("OwnedAgents", () => {
  afterEach(() => {
    cleanup();
    authMock.current = { isLoaded: true, isSignedIn: true };
  });

  it("lists owned agents", async () => {
    apiMock.current = {
      listOwnedAgents: vi.fn().mockResolvedValue({ agents: [heibot] })
    };
    renderWithQuery(<OwnedAgents />);
    expect(await screen.findByText("hei")).toBeInTheDocument();
    expect(screen.getByText("heibot/home")).toBeInTheDocument();
    expect(screen.getByText(/never active/)).toBeInTheDocument();
  });

  it("shows how to get an agent when there are none", async () => {
    apiMock.current = {
      listOwnedAgents: vi.fn().mockResolvedValue({ agents: [] })
    };
    renderWithQuery(<OwnedAgents />);
    expect(await screen.findByText(/No agents yet/)).toBeInTheDocument();
  });

  it("renders nothing when the lookup fails", async () => {
    const listOwnedAgents = vi.fn().mockRejectedValue(new Error("unavailable"));
    apiMock.current = { listOwnedAgents };
    const { container } = renderWithQuery(<OwnedAgents />);
    await waitFor(() => expect(listOwnedAgents).toHaveBeenCalled());
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it("renders nothing when signed out", () => {
    authMock.current = { isLoaded: true, isSignedIn: false };
    const listOwnedAgents = vi.fn();
    apiMock.current = { listOwnedAgents };
    const { container } = renderWithQuery(<OwnedAgents />);
    expect(container).toBeEmptyDOMElement();
    expect(listOwnedAgents).not.toHaveBeenCalled();
  });
});

describe("SlicesList with owned agents", () => {
  afterEach(() => cleanup());

  it("merges the agents' slices into the home list", async () => {
    const listSlices = vi.fn(({ account }: { account: string }) =>
      Promise.resolve({
        slices: [
          {
            id: `slice_${account}_home`,
            ref: { account, slice: "home" },
            definition: { includedPaths: [`/${account}`], visibility: "private" }
          }
        ]
      })
    );
    apiMock.current = {
      listOwnedAgents: vi.fn().mockResolvedValue({ agents: [heibot] }),
      listSlices
    };
    renderWithQuery(<SlicesList />);
    await waitFor(() =>
      expect(listSlices).toHaveBeenCalledWith(
        expect.objectContaining({ account: "heibot" })
      )
    );
    expect(listSlices).toHaveBeenCalledWith(
      expect.objectContaining({ account: "nic" })
    );
    expect(
      await screen.findByText("Slices in your account and in your agents' accounts.")
    ).toBeInTheDocument();
    expect(screen.getAllByText("agent").length).toBeGreaterThan(0);
  });

  it("falls back to your own slices if owned agents cannot load", async () => {
    const listSlices = vi.fn().mockResolvedValue({ slices: [] });
    apiMock.current = {
      listOwnedAgents: vi.fn().mockRejectedValue(new Error("unavailable")),
      listSlices
    };
    renderWithQuery(<SlicesList />);
    await waitFor(() => expect(listSlices).toHaveBeenCalledTimes(1));
    expect(listSlices).toHaveBeenCalledWith(
      expect.objectContaining({ account: "nic" })
    );
  });
});
