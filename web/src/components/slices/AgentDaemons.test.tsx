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
  runtime: "ci",
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
  runtime: "ci",
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

  it("lists registered CI runners with their names, status, and assigned slices without deprecated conversation copy", async () => {
    apiMock.current = { listDaemons: vi.fn().mockResolvedValue({ daemons: [ownCi, botCi] }) };
    renderDaemons();

    expect(await screen.findByText("laptop-ci")).toBeInTheDocument();
    expect(screen.getByText("online")).toBeInTheDocument();
    expect(screen.getByText("nic/app")).toBeInTheDocument();
    expect(screen.getByText(/v0\.4\.8/)).toBeInTheDocument();
    // Deprecated conversation / checks-only runtime copy is gone.
    expect(screen.queryByText(/agent conversations/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/checks only/i)).not.toBeInTheDocument();
    // A renamed runner shows its new name and what it registers as.
    expect(screen.getByText("Main CI")).toBeInTheDocument();
    expect(screen.getByText(/starts as grok-bot-ci/)).toBeInTheDocument();
    expect(screen.getByText("offline")).toBeInTheDocument();
    expect(screen.getByText(/co-owned/)).toBeInTheDocument();
    expect(screen.getByText(/last seen/)).toBeInTheDocument();
    // Management forms stay tucked away until Manage is clicked.
    expect(screen.queryByLabelText("Display name")).not.toBeInTheDocument();
  });

  it("says how to start a CI runner when there are none", async () => {
    apiMock.current = { listDaemons: vi.fn().mockResolvedValue({ daemons: [] }) };
    renderDaemons();
    expect(await screen.findByText(/No CI runners registered yet/)).toBeInTheDocument();
    expect(screen.getAllByText(/gs agent start --ci --name <name>/).length).toBeGreaterThanOrEqual(1);
  });

  it("renames a runner and clears its name via the Manage panel", async () => {
    const renameDaemon = vi.fn().mockResolvedValue({ ...ownCi, displayName: "Laptop" });
    apiMock.current = {
      listDaemons: vi.fn().mockResolvedValue({ daemons: [ownCi, botCi] }),
      renameDaemon
    };
    renderDaemons();

    fireEvent.click(await screen.findByRole("button", { name: "Manage laptop-ci" }));
    const input = screen.getByLabelText("Display name");
    expect(input).toHaveValue("laptop-ci");
    fireEvent.change(input, { target: { value: "  Laptop  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save name" }));
    await waitFor(() => expect(renameDaemon).toHaveBeenCalledWith({ daemonId: "agent_own", displayName: "Laptop" }));

    // A renamed runner can go back to the name it registers with.
    fireEvent.click(screen.getByRole("button", { name: "Manage Main CI" }));
    fireEvent.click(screen.getByRole("button", { name: "Use grok-bot-ci" }));
    await waitFor(() => expect(renameDaemon).toHaveBeenCalledWith({ daemonId: "agent_bot", displayName: "" }));
  });

  it("shows why a rename was refused", async () => {
    apiMock.current = {
      listDaemons: vi.fn().mockResolvedValue({ daemons: [ownCi] }),
      renameDaemon: vi.fn().mockRejectedValue(new Error("another daemon of nic is already shown as Laptop"))
    };
    renderDaemons();

    fireEvent.click(await screen.findByRole("button", { name: "Manage laptop-ci" }));
    fireEvent.change(screen.getByLabelText("Display name"), { target: { value: "Laptop" } });
    fireEvent.click(screen.getByRole("button", { name: "Save name" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("already shown as Laptop");
  });

  it("shares a runner with a slice and stops sharing via the Manage panel", async () => {
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

    expect(await screen.findByText(/Shared with 1 slice/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Manage laptop-ci" }));

    expect(screen.getByText(/Shared with \(their owners can choose this runner for CI\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop sharing laptop-ci with nic/app" }));
    await waitFor(() =>
      expect(unexposeDaemon).toHaveBeenCalledWith({ daemonId: "agent_own", slice: { account: "nic", slice: "app" } })
    );

    const input = screen.getByLabelText("Share laptop-ci with a slice");
    const share = screen.getByRole("button", { name: "Share" });
    expect(share).toBeDisabled();
    fireEvent.change(input, { target: { value: "gitslice:gitslice" } });
    fireEvent.click(share);
    await waitFor(() =>
      expect(exposeDaemon).toHaveBeenCalledWith({ daemonId: "agent_own", slice: { account: "gitslice", slice: "gitslice" } })
    );
  });

  it("says when a runner is shared with no slice inside Manage", async () => {
    apiMock.current = { listDaemons: vi.fn().mockResolvedValue({ daemons: [{ ...ownCi, manageable: true, exposedTo: [] }] }) };
    renderDaemons();
    fireEvent.click(await screen.findByRole("button", { name: "Manage laptop-ci" }));
    expect(screen.getByText(/Not shared with any slice yet/)).toBeInTheDocument();
  });

  it("renders nothing when the runners cannot be listed", async () => {
    apiMock.current = { listDaemons: vi.fn().mockRejectedValue(new Error("unavailable")) };
    const { container } = renderDaemons();
    await waitFor(() => expect(apiMock.current.listDaemons).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });
});
