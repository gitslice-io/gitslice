import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SlicesList } from "./SlicesList";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

const authMock = vi.hoisted(() => ({
  current: { isLoaded: true, isSignedIn: true }
}));

const selectionMock = vi.hoisted(() => ({
  current: {
    account: "nic",
    accounts: ["nic"],
    error: null,
    isLoading: false,
    needsUsername: false,
    subjectId: "user_1"
  }
}));

const searchMock = vi.hoisted(() => ({
  current: {} as { account?: string }
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => authMock.current
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useSearch: () => searchMock.current
}));

vi.mock("../../state/selection", () => ({
  useSelection: () => selectionMock.current
}));

function renderWithQuery(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
  );
}

function sliceFor(account: string) {
  return {
    id: `slice_${account}_home`,
    ref: { account, slice: "home" },
    definition: {
      includedPaths: [`/${account}`],
      visibility: "private"
    }
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });

  return { promise, reject, resolve };
}

describe("SlicesList", () => {
  afterEach(() => {
    cleanup();
    authMock.current = { isLoaded: true, isSignedIn: true };
    selectionMock.current = {
      account: "nic",
      accounts: ["nic"],
      error: null,
      isLoading: false,
      needsUsername: false,
      subjectId: "user_1"
    };
    searchMock.current = {};
  });

  it("renders own slices while owned agents are still loading", async () => {
    const ownedAgents = deferred<{ agents: [] }>();
    const listSlices = vi.fn().mockResolvedValue({ slices: [sliceFor("nic")] });
    apiMock.current = {
      listOwnedAgents: vi.fn(() => ownedAgents.promise),
      listSlices
    };

    renderWithQuery(<SlicesList />);

    expect(await screen.findAllByText("nic:home")).not.toHaveLength(0);
    expect(listSlices).toHaveBeenCalledWith({
      account: "nic",
      cursor: "",
      pageSize: 100
    });
    expect(screen.getByText("Loading your agents' slices…")).toBeInTheDocument();
  });

  it("appends agent slices with an agent badge when agents resolve", async () => {
    const ownedAgents = deferred<{
      agents: Array<{ account: string; agentSubjectId: string }>;
    }>();
    const listSlices = vi.fn(({ account }: { account: string }) =>
      Promise.resolve({ slices: [sliceFor(account)] })
    );
    apiMock.current = {
      listOwnedAgents: vi.fn(() => ownedAgents.promise),
      listSlices
    };

    renderWithQuery(<SlicesList />);
    expect(await screen.findAllByText("nic:home")).not.toHaveLength(0);

    ownedAgents.resolve({
      agents: [{ account: "heibot", agentSubjectId: "agent_1" }]
    });

    expect(await screen.findAllByText("heibot:home")).not.toHaveLength(0);
    expect(screen.getAllByText("agent")).not.toHaveLength(0);
    expect(listSlices).toHaveBeenCalledWith({
      account: "heibot",
      cursor: "",
      pageSize: 100
    });
  });

  it("keeps own slices visible when an agent account cannot load", async () => {
    const listSlices = vi.fn(({ account }: { account: string }) => {
      if (account === "heibot") {
        return Promise.reject(new Error("unavailable"));
      }

      return Promise.resolve({ slices: [sliceFor(account)] });
    });
    apiMock.current = {
      listOwnedAgents: vi.fn().mockResolvedValue({
        agents: [{ account: "heibot", agentSubjectId: "agent_1" }]
      }),
      listSlices
    };

    renderWithQuery(<SlicesList />);

    expect(await screen.findAllByText("nic:home")).not.toHaveLength(0);
    expect(
      await screen.findByText("Could not load slices for heibot.")
    ).toBeInTheDocument();
  });

  it("waits for agent slices instead of showing an empty list", async () => {
    const heibotSlices = deferred<{ slices: ReturnType<typeof sliceFor>[] }>();
    apiMock.current = {
      listOwnedAgents: vi.fn().mockResolvedValue({
        agents: [{ account: "heibot", agentSubjectId: "agent_1" }]
      }),
      listSlices: vi.fn(({ account }: { account: string }) =>
        account === "heibot"
          ? heibotSlices.promise
          : Promise.resolve({ slices: [] })
      )
    };

    renderWithQuery(<SlicesList />);

    await waitFor(() =>
      expect(apiMock.current.listSlices).toHaveBeenCalledTimes(2)
    );
    expect(screen.queryByText("No slices returned")).not.toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();

    heibotSlices.resolve({ slices: [sliceFor("heibot")] });

    expect(await screen.findAllByText("heibot:home")).not.toHaveLength(0);
  });

  it("lists only the explicit account", async () => {
    searchMock.current = { account: "other" };
    const listSlices = vi.fn(({ account }: { account: string }) =>
      Promise.resolve({ slices: [sliceFor(account)] })
    );
    apiMock.current = {
      listOwnedAgents: vi.fn().mockResolvedValue({
        agents: [{ account: "heibot", agentSubjectId: "agent_1" }]
      }),
      listSlices
    };

    renderWithQuery(<SlicesList />);

    expect(await screen.findAllByText("other:home")).not.toHaveLength(0);
    expect(screen.queryByText("heibot:home")).not.toBeInTheDocument();
    expect(listSlices).toHaveBeenCalledTimes(1);
    expect(listSlices).toHaveBeenCalledWith({
      account: "other",
      cursor: "",
      pageSize: 100
    });
  });
});
