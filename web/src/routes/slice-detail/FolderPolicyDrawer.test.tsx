import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../api/useApi";
import { FolderPolicyDrawer } from "./FolderPolicyDrawer";

const apiMock = vi.hoisted(() => ({ current: {} as ApiClient }));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>
}));

function renderWithQuery(element: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{element}</QueryClientProvider>);
}

afterEach(cleanup);

describe("FolderPolicyDrawer", () => {
  it("renders checks, triggers, secrets with inheritance, owners, and CI runners across accessible tabs", async () => {
    const runTrigger = vi.fn().mockResolvedValue({ id: "trun_9" });
    const createSecret = vi.fn().mockResolvedValue({ id: "sec_3", name: "NEW_KEY" });
    const getFolderPolicy = vi.fn().mockResolvedValue({
      folder: "/acme/web",
      account: "acme",
      commitId: "commit_1",
      ownerConfigPath: "/acme/.gitslice/owners.yaml",
      ownerSourceFolder: "/acme",
      ownerRules: [
        { paths: ["**"], owners: ["alice", "@acme/web"], require: 1, requireHuman: 1 }
      ],
      checks: [
        { id: "lint", name: "lint", folder: "/acme", configPath: "/acme/.gitslice/checks.yaml", run: "go vet ./...", required: true },
        { id: "web/build", name: "build", folder: "/acme/web", configPath: "/acme/web/.gitslice/checks.yaml", run: "npm run build", required: false }
      ],
      triggers: [
        { id: "web/deploy", name: "deploy", folder: "/acme/web", on: ["push", "manual"], action: "http", httpUrl: "https://hooks.example.com/deploy" }
      ],
      secrets: [
        { name: "SHARED_KEY", secretId: "sec_1", sourceFolder: "/acme", paths: ["/acme"] },
        { name: "WEB_TOKEN", secretId: "sec_2", sourceFolder: "/acme/web", paths: ["/acme/web"] }
      ],
      canManageSecrets: true,
      ciDaemonIds: ["daemon_1"],
      ciDaemonNames: ["org-ci"],
      problems: []
    });
    const api = {
      getFolderPolicy,
      listTriggerRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listTriggerRunLogs: vi.fn().mockResolvedValue({ logs: [] }),
      runTrigger,
      createSecret,
      listSlices: vi.fn().mockResolvedValue({ slices: [] }),
      getRef: vi.fn().mockResolvedValue({ commitId: "commit_1" }),
      listDirectory: vi.fn().mockResolvedValue({ entries: [] })
    } as unknown as ApiClient;
    apiMock.current = api;

    renderWithQuery(<FolderPolicyDrawer api={api} folder="/acme/web" onClose={() => {}} open />);

    // Automation tab is active by default: checks (with Required + inheritance) & triggers.
    expect(await screen.findByText("lint")).toBeInTheDocument();
    expect(screen.getByText("Required")).toBeInTheDocument();
    expect(screen.getAllByText("Inherited from /acme").length).toBeGreaterThan(0);
    expect(screen.getByText("web/build")).toBeInTheDocument();
    expect(screen.getByText("web/deploy")).toBeInTheDocument();
    expect(screen.getByText("https://hooks.example.com/deploy")).toBeInTheDocument();

    // Manual trigger run works.
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(runTrigger).toHaveBeenCalledWith({ account: "acme", triggerId: "web/deploy" }));

    // Keyboard navigation moves focus + selection to Secrets tab (ArrowRight).
    const automationTab = screen.getByRole("tab", { name: /Automation/ });
    const secretsTab = screen.getByRole("tab", { name: /Secrets/ });
    const ownersTab = screen.getByRole("tab", { name: /Owners & Runners/ });

    fireEvent.keyDown(automationTab, { key: "ArrowRight" });
    expect(secretsTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("SHARED_KEY")).toBeInTheDocument();
    expect(screen.getByText("WEB_TOKEN")).toBeInTheDocument();

    // Admin can add a folder secret inline pre-scoped to /acme/web.
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    fireEvent.change(screen.getByLabelText(/^Name/i), { target: { value: "NEW_KEY" } });
    fireEvent.change(screen.getByLabelText(/^Value/i), { target: { value: "s3cr3t" } });
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    await waitFor(() =>
      expect(createSecret).toHaveBeenCalledWith({
        account: "acme",
        name: "NEW_KEY",
        scope: { paths: ["/acme/web"] },
        value: "s3cr3t"
      })
    );

    // Switch to Owners & Runners tab.
    fireEvent.click(ownersTab);
    expect(ownersTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("1 approval · 1 human")).toBeInTheDocument();
    expect(screen.getByText("alice, @acme/web")).toBeInTheDocument();
    expect(screen.getByText("org-ci")).toBeInTheDocument();
  });

  it("does not load policy until opened", () => {
    const getFolderPolicy = vi.fn();
    renderWithQuery(
      <FolderPolicyDrawer
        api={{ getFolderPolicy } as unknown as ApiClient}
        folder="/acme/web"
        onClose={() => {}}
        open={false}
      />
    );
    expect(getFolderPolicy).not.toHaveBeenCalled();
  });
});
