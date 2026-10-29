import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ChecksPanel } from "./ChecksPanel";
import { TriggerRunsPanel } from "./TriggerRunsPanel";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

function renderWithQuery(element: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{element}</QueryClientProvider>);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("TriggerRunsPanel", () => {
  it("lists the patchset's runs with their logs, and reruns or cancels them", async () => {
    const rerunTriggerRun = vi.fn().mockResolvedValue({ id: "trun_3" });
    const cancelTriggerRun = vi.fn().mockResolvedValue({ id: "trun_2", status: "canceled" });
    apiMock.current = {
      listTriggerRuns: vi.fn().mockResolvedValue({
        runs: [
          { id: "trun_1", triggerId: "payment/ci", status: "succeeded", eventKind: "changeset.updated", action: "worker", patchsetId: "ps_2" },
          { id: "trun_2", triggerId: "payment/lint", status: "waiting", eventKind: "changeset.updated", action: "worker", patchsetId: "ps_2" },
          { id: "trun_old", triggerId: "payment/ci", status: "canceled", eventKind: "changeset.updated", action: "worker", patchsetId: "ps_1" }
        ]
      }),
      listTriggerRunLogs: vi.fn().mockResolvedValue({ logs: [{ seq: "1", chunk: "built in 3s\n" }] }),
      rerunTriggerRun,
      cancelTriggerRun
    };
    renderWithQuery(<TriggerRunsPanel changesetId="cs_1" patchsetId="ps_2" />);

    expect(await screen.findByText("payment/ci")).toBeInTheDocument();
    expect(screen.getByText("2 runs")).toBeInTheDocument();
    expect(screen.queryByText("trun_old")).not.toBeInTheDocument();

    fireEvent.click(screen.getByText("payment/ci"));
    expect(await screen.findByText("built in 3s")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Rerun" }));
    await waitFor(() => expect(rerunTriggerRun).toHaveBeenCalledWith({ runId: "trun_1" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(cancelTriggerRun).toHaveBeenCalledWith({ runId: "trun_2" }));
  });

  it("shows nothing when no trigger ran", async () => {
    const listTriggerRuns = vi.fn().mockResolvedValue({ runs: [] });
    apiMock.current = { listTriggerRuns };
    const { container } = renderWithQuery(<TriggerRunsPanel changesetId="cs_1" patchsetId="ps_2" />);
    await waitFor(() => expect(listTriggerRuns).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });
});

describe("ChecksPanel with a trigger's check", () => {
  it("links details, shows annotations and the run's log, and reruns the trigger", async () => {
    const rerunTriggerRun = vi.fn().mockResolvedValue({ id: "trun_9" });
    const rerunCheck = vi.fn();
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            id: "run_1",
            checkName: "payment/ci",
            status: "failed",
            provenance: "trigger",
            triggerRunId: "trun_1",
            summary: "2 failures",
            detailsUrl: "https://ci.example.com/build/7",
            annotations: [{ path: "/acme/payment/main.go", startLine: 3, level: "failure", message: "undefined: x" }]
          },
          { id: "run_2", checkName: "payment/evil", status: "passed", provenance: "trigger", triggerRunId: "trun_2", detailsUrl: "javascript:alert(1)" }
        ]
      }),
      listTriggerRunLogs: vi.fn().mockResolvedValue({ logs: [{ seq: "1", chunk: "build 7 failed\n" }] }),
      rerunTriggerRun,
      rerunCheck,
      streamCheckRun: vi.fn(async function* () {})
    };
    renderWithQuery(<ChecksPanel changesetId="cs_1" patchsetId="ps_2" />);

    const details = await screen.findAllByRole("link", { name: "Details" });
    expect(details).toHaveLength(1);
    expect(details[0]).toHaveAttribute("href", "https://ci.example.com/build/7");
    expect(screen.getAllByText("trigger")).toHaveLength(2);

    fireEvent.click(screen.getByRole("button", { name: /payment\/ci/ }));
    expect(await screen.findByText("undefined: x")).toBeInTheDocument();
    expect(screen.getByText("/acme/payment/main.go:3")).toBeInTheDocument();
    expect(await screen.findByText("build 7 failed")).toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: "Rerun" })[0]);
    await waitFor(() => expect(rerunTriggerRun).toHaveBeenCalledWith({ runId: "trun_1" }));
    expect(rerunCheck).not.toHaveBeenCalled();
  });
});
