import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../../api/errors";
import { ChecksPanel } from "./ChecksPanel";

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

describe("ChecksPanel", () => {
  beforeEach(() => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            checkName: "unit tests",
            changesetId: "cs_1",
            exitCode: 0,
            id: "run_passed",
            patchsetId: "ps_2",
            provenance: "self",
            status: "passed",
            summary: "All tests passed."
          },
          {
            checkName: "lint",
            changesetId: "cs_1",
            exitCode: 1,
            id: "run_failed",
            patchsetId: "ps_2",
            provenance: "ci",
            status: "failed",
            summary: "golangci-lint failed."
          }
        ]
      }),
      rerunCheck: vi.fn().mockResolvedValue({ id: "run_rerun" }),
      streamCheckRun: vi.fn(async function* () {})
    };
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("renders passed and failed check runs with status and provenance", async () => {
    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    const unitTests = await screen.findByRole("button", {
      name: /unit tests/i
    });
    const lint = await screen.findByRole("button", { name: /lint/i });

    expect(within(unitTests).getByText("Passed")).toBeInTheDocument();
    expect(within(unitTests).getByText("self")).toBeInTheDocument();
    expect(within(unitTests).getByText("exit 0")).toBeInTheDocument();
    expect(within(unitTests).getByText("All tests passed.")).toBeInTheDocument();
    expect(within(lint).getByText("Failed")).toBeInTheDocument();
    expect(within(lint).getByText("ci")).toBeInTheDocument();
    expect(within(lint).getByText("exit 1")).toBeInTheDocument();
    expect(within(lint).getByText("golangci-lint failed.")).toBeInTheDocument();
    await waitFor(() =>
      expect(apiMock.current.listCheckRuns).toHaveBeenCalledWith({
        changesetId: "cs_1",
        patchsetId: "ps_2"
      })
    );
  });

  it("reruns a terminal check and refreshes the run list", async () => {
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            checkName: "lint",
            changesetId: "cs_1",
            exitCode: 1,
            id: "run_failed",
            patchsetId: "ps_2",
            provenance: "ci",
            status: "failed",
            summary: "golangci-lint failed."
          }
        ]
      }),
      rerunCheck: vi.fn().mockResolvedValue({ id: "run_new" }),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    fireEvent.click(await screen.findByRole("button", { name: "Rerun" }));

    await waitFor(() =>
      expect(apiMock.current.rerunCheck).toHaveBeenCalledWith({
        runId: "run_failed"
      })
    );
    await waitFor(() =>
      expect(apiMock.current.listCheckRuns).toHaveBeenCalledTimes(2)
    );
  });

  it("does not request or render checks for signed-out viewers", () => {
    authMock.current = { isLoaded: true, isSignedIn: false };

    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    expect(apiMock.current.listCheckRuns).not.toHaveBeenCalled();
    expect(screen.queryByText("Checks")).not.toBeInTheDocument();
  });

  it.each([401, 403])("hides access errors with status %i", async (status) => {
    apiMock.current = {
      listCheckRuns: vi.fn().mockRejectedValue(
        new RpcError(status, { message: "no access" })
      ),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    const { queryClient } = renderRoute(
      <ChecksPanel changesetId="cs_1" patchsetId="ps_2" />
    );

    await waitFor(() =>
      expect(
        queryClient.getQueryState(["checkRuns", "cs_1", "ps_2"])?.status
      ).toBe("error")
    );
    expect(screen.queryByText("no access")).not.toBeInTheDocument();
    expect(screen.queryByText("Checks")).not.toBeInTheDocument();
  });

  it("still shows other errors", async () => {
    apiMock.current = {
      listCheckRuns: vi.fn().mockRejectedValue(
        new RpcError(500, { message: "checks backend down" })
      ),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    expect(await screen.findByText("checks backend down")).toBeInTheDocument();
  });
});

function renderRoute(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });

  return {
    ...render(
      <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
    ),
    queryClient
  };
}
