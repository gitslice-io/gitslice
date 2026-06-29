import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
            status: "failed",
            summary: "golangci-lint failed."
          }
        ]
      }),
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
