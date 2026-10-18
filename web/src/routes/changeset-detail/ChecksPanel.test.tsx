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

import { RpcError } from "../../api/client";
import { ChecksPanel } from "./ChecksPanel";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

describe("ChecksPanel", () => {
  beforeEach(() => {
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
            daemonId: "agent_f83a",
            daemonName: "Main CI",
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
    vi.useRealTimers();
  });

  it("asks signed-out viewers to sign in instead of showing an error", async () => {
    apiMock.current.listCheckRuns = vi
      .fn()
      .mockRejectedValue(new RpcError(401, { code: 16, message: "missing subject" }));
    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    expect(await screen.findByText("Sign in to see checks")).toBeInTheDocument();
    expect(screen.queryByText("missing subject")).not.toBeInTheDocument();
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
    // A run that a daemon ran says which, by name; one that ran in the author's
    // workspace has none.
    expect(within(lint).getByText("on Main CI")).toBeInTheDocument();
    expect(within(unitTests).queryByText(/^on /)).not.toBeInTheDocument();
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

  it("explains when a queued check is waiting for an offline daemon", async () => {
    const fiveMinutesAgo = new Date(Date.now() - 5 * 60 * 1000).toISOString();
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            checkName: "e2e",
            daemonId: "agent_ci",
            daemonLastSeenAt: fiveMinutesAgo,
            daemonName: "Main CI",
            // JSON leaves daemonOnline out when it is false.
            id: "run_queued",
            status: "queued"
          }
        ]
      }),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    expect(
      await screen.findByText("Waiting for Main CI, offline, last seen 5m ago")
    ).toBeInTheDocument();
  });

  it("says nothing when the server does not report daemon status", async () => {
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [{ checkName: "e2e", daemonId: "agent_ci", daemonName: "Main CI", id: "run_queued", status: "queued" }]
      }),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    await screen.findByRole("button", { name: /e2e/i });
    expect(screen.queryByText(/^Waiting for Main CI/)).not.toBeInTheDocument();
  });

  it("does not show an offline notice for a queued daemon that is online", async () => {
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            checkName: "e2e",
            daemonId: "agent_ci",
            daemonLastSeenAt: "2026-10-18T18:55:00Z",
            daemonName: "Main CI",
            daemonOnline: true,
            id: "run_queued",
            status: "queued"
          }
        ]
      }),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderRoute(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    await screen.findByRole("button", { name: /e2e/i });
    expect(screen.queryByText(/^Waiting for Main CI/)).not.toBeInTheDocument();
  });
});

function renderRoute(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });

  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
  );
}
