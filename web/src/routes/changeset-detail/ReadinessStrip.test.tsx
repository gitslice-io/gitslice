import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ReadinessStrip, summarizeChecksAndTriggers } from "./ReadinessStrip";
import { summarizeApprovals } from "./approvalSummary";

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

describe("summarizeChecksAndTriggers & ReadinessStrip", () => {
  it("summarizes passed, running, failed, and signed-out states concisely", () => {
    expect(summarizeChecksAndTriggers([], [], true)).toEqual({
      label: "Sign in for checks",
      tone: "muted",
      live: false
    });
    expect(
      summarizeChecksAndTriggers(
        [
          { id: "c1", status: "passed" },
          { id: "c2", status: "passed" }
        ],
        [],
        false
      )
    ).toEqual({
      label: "2/2 checks passed",
      tone: "passed",
      live: false
    });
    expect(
      summarizeChecksAndTriggers(
        [
          { id: "c1", status: "passed" },
          { id: "c2", status: "running" }
        ],
        [],
        false
      )
    ).toEqual({
      label: "Checks 1/2",
      tone: "running",
      live: true
    });
    expect(
      summarizeChecksAndTriggers(
        [
          { id: "c1", status: "passed" },
          { id: "c2", status: "failed" }
        ],
        [],
        false
      )
    ).toEqual({
      label: "1/2 checks failed",
      tone: "failed",
      live: false
    });
  });

  it("combines approvals and checks into one collapsible disclosure without duplicate Approve buttons", async () => {
    apiMock.current = {
      listCheckRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            id: "chk_1",
            checkName: "gitslice/test",
            status: "passed",
            provenance: "ci",
            automationRunId: "trun_1"
          }
        ]
      }),
      listAutomationRuns: vi.fn().mockResolvedValue({
        runs: [
          {
            id: "trun_1",
            automationId: "gitslice/ci",
            status: "succeeded",
            action: "check",
            patchsetId: "ps_1"
          }
        ]
      })
    };

    const approvalSummary = summarizeApprovals({
      id: "cs_1",
      author: "mike",
      currentPatchsetId: "ps_1",
      status: "draft",
      approvalStatus: {
        satisfied: false,
        requirements: [
          {
            kind: "path_owners",
            satisfied: false,
            required: 1,
            current: 0,
            label: "Path owners (slices, gitslice-operator, antigravity-nic)",
            detail: "/gitslice/gitslice/design/10_execution_log.md (+23 more)"
          }
        ]
      }
    });

    renderWithQuery(
      <ReadinessStrip
        approvalSummary={approvalSummary}
        changesetId="cs_1"
        patchsetId="ps_1"
        pathPrefix="/gitslice/gitslice/"
      />
    );

    expect(screen.getByText("Needs approval (0/1)")).toBeInTheDocument();
    expect(await screen.findByText("1/1 check passed")).toBeInTheDocument();
    expect(screen.getByText("design/10_execution_log.md (+23 more)")).toBeInTheDocument();
    // No duplicate Approve button or Waiting for approvals banner inside embedded view.
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByText("Waiting for approvals")).not.toBeInTheDocument();
  });
});
