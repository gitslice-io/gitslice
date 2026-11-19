import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AccountCIDaemonPanel } from "./AccountCIDaemonPanel";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

function renderWithQuery(element: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } }
  });
  return render(<QueryClientProvider client={client}>{element}</QueryClientProvider>);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("AccountCIDaemonPanel", () => {
  it("lists and updates the account CI runner pool for admins", async () => {
    const setAccountCIDaemon = vi.fn().mockResolvedValue({
      account: "acme",
      daemonIds: ["daemon_1"],
      daemonNames: ["main-runner"]
    });
    apiMock.current = {
      getAccountCIDaemons: vi.fn().mockResolvedValue({
        account: "acme",
        daemonIds: [],
        daemonNames: []
      }),
      listDaemons: vi.fn().mockResolvedValue({
        daemons: [
          { id: "daemon_1", name: "main-runner", status: "online", runtime: "ci", manageable: true },
          { id: "daemon_2", name: "backup-runner", status: "online", runtime: "ci", manageable: true }
        ]
      }),
      setAccountCIDaemon
    };

    renderWithQuery(<AccountCIDaemonPanel account="acme" canEdit />);

    expect(await screen.findByText("No account CI runners configured")).toBeInTheDocument();
    const select = await screen.findByLabelText("Account CI runner");
    fireEvent.change(select, { target: { value: "daemon_1" } });

    await waitFor(() =>
      expect(setAccountCIDaemon).toHaveBeenCalledWith({ account: "acme", daemonId: "daemon_1" })
    );
    expect(await screen.findByText("CI runner pool updated.")).toBeInTheDocument();
  });
});
