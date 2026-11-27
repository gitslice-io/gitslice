import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../../api/rpcError";
import { groupTriggersAndChecks } from "./automationGrouping";
import { TriggersChecksPanel } from "./TriggersChecksPanel";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

function renderWithQuery(element: ReactElement) {
  const client = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });
  return render(<QueryClientProvider client={client}>{element}</QueryClientProvider>);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("groupTriggersAndChecks", () => {
  it("nests checks under their parent trigger, collapses 0-check succeeded check triggers, and keeps standalone checks", () => {
    const grouped = groupTriggersAndChecks(
      [
        { id: "trun_web", automationId: "gitslice/web/ci", status: "succeeded", action: "check", patchsetId: "ps_1" },
        { id: "trun_root", automationId: "gitslice/ci", status: "succeeded", action: "check", patchsetId: "ps_1" },
        { id: "trun_hook", automationId: "gitslice/notify", status: "succeeded", action: "http", patchsetId: "ps_1" },
        { id: "trun_err", automationId: "gitslice/broken", status: "errored", action: "check", patchsetId: "ps_1" },
        { id: "trun_old", automationId: "gitslice/web/ci", status: "canceled", action: "check", patchsetId: "ps_0" }
      ],
      [
        { id: "chk_web", checkName: "gitslice/web/build", status: "passed", provenance: "ci", automationRunId: "trun_web" },
        { id: "chk_solo", checkName: "manual/lint", status: "passed", provenance: "self" }
      ],
      "ps_1"
    );

    expect(grouped.primaryTriggers.map((g) => g.trigger.id)).toEqual(["trun_err", "trun_web", "trun_hook"]);
    expect(grouped.primaryTriggers.find((g) => g.trigger.id === "trun_web")?.checks.map((c) => c.id)).toEqual([
      "chk_web"
    ]);
    expect(grouped.skippedTriggers.map((t) => t.id)).toEqual(["trun_root"]);
    expect(grouped.standaloneChecks.map((c) => c.id)).toEqual(["chk_solo"]);
  });
});

describe("TriggersChecksPanel", () => {
  it("nests spawned checks inside their trigger and collapses 0-check no-op triggers", async () => {
    const rerunCheck = vi.fn().mockResolvedValue({ id: "chk_new" });
    const rerunAutomationRun = vi.fn().mockResolvedValue({ id: "trun_new" });
    apiMock.current = {
      listAutomationRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            id: "trun_web",
            automationId: "gitslice/web/ci",
            status: "succeeded",
            eventKind: "changeset.updated",
            action: "check",
            patchsetId: "ps_1"
          },
          {
            id: "trun_root",
            automationId: "gitslice/ci",
            status: "succeeded",
            eventKind: "changeset.updated",
            action: "check",
            patchsetId: "ps_1"
          }
        ]
      }),
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            id: "chk_build",
            checkName: "gitslice/web/build",
            status: "passed",
            provenance: "ci",
            daemonName: "Main CI",
            exitCode: 0,
            automationRunId: "trun_web"
          }
        ]
      }),
      listAutomationRunLogs: vi.fn().mockResolvedValue({ logs: [{ seq: "1", chunk: "dispatched 1 check\n" }] }),
      rerunCheck,
      rerunAutomationRun,
      streamCheckRun: vi.fn(async function* () {
        yield { seq: "1", chunk: "vite build ok\n" };
      })
    };

    renderWithQuery(<TriggersChecksPanel changesetId="cs_1" patchsetId="ps_1" />);

    expect(await screen.findByText("gitslice/web/ci")).toBeInTheDocument();
    expect(screen.getByText("gitslice/web/build")).toBeInTheDocument();
    expect(screen.getByText("1 trigger · 1 check")).toBeInTheDocument();

    // Skipped 0-check trigger is summarized in a collapsed footer until expanded.
    expect(screen.getByText(/1 skipped trigger/)).toBeInTheDocument();
    expect(screen.queryByText("View log")).not.toBeInTheDocument();

    // Expand nested check logs inline.
    fireEvent.click(screen.getByRole("button", { name: "View logs" }));
    expect(await screen.findByText(/vite build ok/)).toBeInTheDocument();

    // Expand skipped trigger footer to inspect gitslice/ci.
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(screen.getByRole("button", { name: "View log" })).toBeInTheDocument();

    // Rerun the nested CI check vs the trigger.
    const webTriggerRow = screen.getByText("gitslice/web/ci").closest("li")!;
    const rerunButtons = within(webTriggerRow).getAllByRole("button", { name: "Rerun" });
    fireEvent.click(rerunButtons[0]);
    await waitFor(() => expect(rerunAutomationRun).toHaveBeenCalledWith({ runId: "trun_web" }));
    fireEvent.click(rerunButtons[1]);
    await waitFor(() => expect(rerunCheck).toHaveBeenCalledWith({ runId: "chk_build" }));
  });

  it("shows standalone checks under a Checks header when no triggers ran", async () => {
    apiMock.current = {
      listAutomationRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            id: "run_solo",
            checkName: "backend/test",
            status: "passed",
            provenance: "self",
            exitCode: 0
          }
        ]
      }),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderWithQuery(<TriggersChecksPanel changesetId="cs_1" patchsetId="ps_1" />);

    expect(await screen.findByRole("heading", { name: "Checks" })).toBeInTheDocument();
    expect(screen.getByText("backend/test")).toBeInTheDocument();
    expect(screen.queryByText("Other checks")).not.toBeInTheDocument();
  });

  it("tells signed-out viewers to sign in without querying the API", () => {
    const listCheckRuns = vi.fn();
    const listAutomationRuns = vi.fn();
    apiMock.current = { listCheckRuns, listAutomationRuns };

    renderWithQuery(<TriggersChecksPanel changesetId="cs_1" patchsetId="ps_1" signedOut />);

    expect(screen.getByText("Sign in to see checks")).toBeInTheDocument();
    expect(listCheckRuns).not.toHaveBeenCalled();
    expect(listAutomationRuns).not.toHaveBeenCalled();
  });

  it("still renders checks gracefully when trigger runs fail with 503", async () => {
    apiMock.current = {
      listAutomationRuns: vi
        .fn()
        .mockRejectedValue(new RpcError(503, { code: 14, message: "unavailable" })),
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [{ id: "run_1", checkName: "unit", status: "passed", provenance: "ci" }]
      }),
      rerunCheck: vi.fn(),
      streamCheckRun: vi.fn(async function* () {})
    };

    renderWithQuery(<TriggersChecksPanel changesetId="cs_1" patchsetId="ps_1" />);
    expect(await screen.findByText("unit")).toBeInTheDocument();
  });
});
