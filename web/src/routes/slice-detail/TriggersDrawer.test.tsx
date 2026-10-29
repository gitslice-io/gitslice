import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../api/useApi";
import { triggersFolder } from "../SliceDetailPage";
import { TriggersDrawer } from "./TriggersDrawer";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>
}));

function renderWithQuery(element: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{element}</QueryClientProvider>);
}

afterEach(cleanup);

describe("TriggersDrawer", () => {
  it("lists the folder's triggers, their runs and problems, and runs a manual one", async () => {
    const runTrigger = vi.fn().mockResolvedValue({ id: "trun_9" });
    const listTriggerRuns = vi.fn().mockImplementation(({ triggerId }) =>
      Promise.resolve({
        runs:
          triggerId === "web/ci"
            ? [{ id: "trun_1", status: "failed", eventKind: "changeset.updated", changesetId: "cs_abcdef1234567890", error: "build broke" }]
            : []
      })
    );
    const api = {
      listTriggers: vi.fn().mockResolvedValue({
        triggers: [
          { id: "web/ci", on: ["changeset.updated"], action: "check", checks: ["web/build"], paths: ["**/*.ts"] },
          { id: "deploy", on: ["push", "manual"], action: "http", httpUrl: "https://hooks.example.com/deploy" }
        ],
        problems: [{ file: "/acme/web/app/.gitslice/triggers.yaml", message: "trigger x: unknown event" }]
      }),
      listTriggerRuns,
      listTriggerRunLogs: vi.fn().mockResolvedValue({ logs: [] }),
      runTrigger
    } as unknown as ApiClient;
    renderWithQuery(<TriggersDrawer api={api} folder="/acme/web/app" onClose={() => {}} open />);

    expect(await screen.findByText("web/ci")).toBeInTheDocument();
    expect(screen.getByText("web/build")).toBeInTheDocument();
    expect(screen.getByText("https://hooks.example.com/deploy")).toBeInTheDocument();
    expect(screen.getByText(/does not parse/)).toBeInTheDocument();
    expect(await screen.findByText("build broke")).toBeInTheDocument();
    expect(listTriggerRuns).toHaveBeenCalledWith({ account: "acme", triggerId: "web/ci", limit: 5 });

    // Only the trigger that listens for manual has a Run button.
    const runs = screen.getAllByRole("button", { name: "Run" });
    expect(runs).toHaveLength(1);
    fireEvent.click(runs[0]);
    await waitFor(() => expect(runTrigger).toHaveBeenCalledWith({ account: "acme", triggerId: "deploy" }));
  });

  it("does not load until it is opened", () => {
    const listTriggers = vi.fn();
    renderWithQuery(<TriggersDrawer api={{ listTriggers } as unknown as ApiClient} folder="/acme" onClose={() => {}} open={false} />);
    expect(listTriggers).not.toHaveBeenCalled();
  });
});

describe("triggersFolder", () => {
  it("is the folder, a file's folder, or the slice's first folder", () => {
    expect(triggersFolder("/acme/web/src", false, ["/acme/web"])).toBe("/acme/web/src");
    expect(triggersFolder("/acme/web/src/app.ts", true, ["/acme/web"])).toBe("/acme/web/src");
    expect(triggersFolder("", false, ["/acme/web"])).toBe("/acme/web");
    expect(triggersFolder("", false, [])).toBe("");
  });
});
