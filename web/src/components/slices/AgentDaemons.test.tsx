import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AgentDaemons } from "./AgentDaemons";

const apiMock = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));

vi.mock("../../api/useApi", () => ({ useApi: () => apiMock.current }));
vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({ isLoaded: true, isSignedIn: true })
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: ReactNode; to?: string }) => <a href={to}>{children}</a>
}));

function renderDaemons() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <AgentDaemons />
    </QueryClientProvider>
  );
}

const ownCi = {
  id: "agent_own",
  account: "nic",
  name: "laptop-ci",
  runtime: "codex",
  version: "0.4.8",
  status: "online",
  mine: true,
  ciSlices: [{ account: "nic", slice: "app" }]
};
const botCi = {
  id: "agent_bot",
  account: "grok-bot",
  name: "grok-bot-ci",
  displayName: "Main CI",
  runtime: "codex",
  status: "offline",
  lastSeenAt: new Date(Date.now() - 3 * 3600 * 1000).toISOString(),
  mine: false,
  ciSlices: []
};

describe("AgentDaemons", () => {
  afterEach(() => {
    cleanup();
    apiMock.current = {};
  });

  it("lists the daemons with their names, status and the slices that use them", async () => {
    apiMock.current = { listDaemons: vi.fn().mockResolvedValue({ daemons: [ownCi, botCi] }) };
    renderDaemons();

    expect(await screen.findByText("laptop-ci")).toBeInTheDocument();
    expect(screen.getByText("online")).toBeInTheDocument();
    expect(screen.getByText("nic/app")).toBeInTheDocument();
    // A renamed daemon shows its new name and says what it registers as.
    expect(screen.getByText("Main CI")).toBeInTheDocument();
    expect(screen.getByText(/starts as grok-bot-ci/)).toBeInTheDocument();
    expect(screen.getByText("offline")).toBeInTheDocument();
    expect(screen.getByText(/an agent you co-own/)).toBeInTheDocument();
    expect(screen.getByText(/last seen/)).toBeInTheDocument();
  });

  it("labels CI-only daemons as checks only", async () => {
    apiMock.current = {
      listDaemons: vi.fn().mockResolvedValue({
        daemons: [{ ...ownCi, runtime: "ci" }]
      })
    };
    renderDaemons();

    expect(await screen.findByText(/checks only 0\.4\.8/)).toBeInTheDocument();
    expect(screen.queryByText(/codex 0\.4\.8/)).not.toBeInTheDocument();
  });

  it("says how to start a daemon when there are none", async () => {
    apiMock.current = { listDaemons: vi.fn().mockResolvedValue({ daemons: [] }) };
    renderDaemons();
    expect(await screen.findByText(/No daemons yet/)).toBeInTheDocument();
  });

  it("renames a daemon and clears its name", async () => {
    const renameDaemon = vi.fn().mockResolvedValue({ ...ownCi, displayName: "Laptop" });
    apiMock.current = {
      listDaemons: vi.fn().mockResolvedValue({ daemons: [ownCi, botCi] }),
      renameDaemon
    };
    renderDaemons();

    fireEvent.click(await screen.findByRole("button", { name: "Rename laptop-ci" }));
    const input = screen.getByLabelText("Name");
    expect(input).toHaveValue("laptop-ci");
    fireEvent.change(input, { target: { value: "  Laptop  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(renameDaemon).toHaveBeenCalledWith({ daemonId: "agent_own", displayName: "Laptop" }));

    // A renamed daemon can go back to the name it registers with.
    fireEvent.click(await screen.findByRole("button", { name: "Rename Main CI" }));
    fireEvent.click(screen.getByRole("button", { name: "Use grok-bot-ci" }));
    await waitFor(() => expect(renameDaemon).toHaveBeenCalledWith({ daemonId: "agent_bot", displayName: "" }));
  });

  it("shows why a rename was refused", async () => {
    apiMock.current = {
      listDaemons: vi.fn().mockResolvedValue({ daemons: [ownCi] }),
      renameDaemon: vi.fn().mockRejectedValue(new Error("another daemon of nic is already shown as Laptop"))
    };
    renderDaemons();

    fireEvent.click(await screen.findByRole("button", { name: "Rename laptop-ci" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Laptop" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("already shown as Laptop");
  });

  it("exposes a daemon to a slice and withdraws it", async () => {
    const exposeDaemon = vi.fn().mockResolvedValue({ ...ownCi });
    const unexposeDaemon = vi.fn().mockResolvedValue({ ...ownCi });
    apiMock.current = {
      listDaemons: vi.fn().mockResolvedValue({
        daemons: [{ ...ownCi, manageable: true, exposedTo: [{ account: "nic", slice: "app" }] }]
      }),
      exposeDaemon,
      unexposeDaemon
    };
    renderDaemons();

    expect(await screen.findByText(/Exposed to \(their owners can choose it/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop exposing laptop-ci to nic/app" }));
    await waitFor(() =>
      expect(unexposeDaemon).toHaveBeenCalledWith({ daemonId: "agent_own", slice: { account: "nic", slice: "app" } })
    );

    const input = screen.getByLabelText("Expose laptop-ci to a slice");
    const expose = screen.getByRole("button", { name: "Expose" });
    expect(expose).toBeDisabled();
    fireEvent.change(input, { target: { value: "gitslice:gitslice" } });
    fireEvent.click(expose);
    await waitFor(() =>
      expect(exposeDaemon).toHaveBeenCalledWith({ daemonId: "agent_own", slice: { account: "gitslice", slice: "gitslice" } })
    );
  });

  it("says when a daemon is exposed to no slice", async () => {
    apiMock.current = { listDaemons: vi.fn().mockResolvedValue({ daemons: [{ ...ownCi, manageable: true, exposedTo: [] }] }) };
    renderDaemons();
    expect(await screen.findByText(/Not exposed to any slice/)).toBeInTheDocument();
  });

  it("renders nothing when the daemons cannot be listed", async () => {
    apiMock.current = { listDaemons: vi.fn().mockRejectedValue(new Error("unavailable")) };
    const { container } = renderDaemons();
    await waitFor(() => expect(apiMock.current.listDaemons).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });
});
