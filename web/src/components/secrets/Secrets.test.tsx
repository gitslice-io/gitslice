import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AccountSecretsPanel } from "./AccountSecretsPanel";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a>
}));

function renderWithQuery(element: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>{element}</QueryClientProvider>);
}

// The form's submit button, not the panel's header button of the same name.
function lastButton(name: string) {
  const buttons = screen.getAllByRole("button", { name });
  return buttons[buttons.length - 1];
}

const acmeSlices = {
  slices: [
    { definition: { includedPaths: ["/acme/api"] }, ref: { account: "acme", slice: "api" } },
    { definition: { includedPaths: ["/acme/web", "/acme/shared/ui"] }, ref: { account: "acme", slice: "web" } }
  ]
};

describe("AccountSecretsPanel", () => {
  afterEach(cleanup);

  it("lists secrets with their folders and never a value", async () => {
    apiMock.current = {
      listSecrets: vi.fn().mockResolvedValue({
        secrets: [
          { id: "sec_1", name: "DEPLOY_TOKEN", scope: { paths: ["/acme/api", "/acme/web"] }, updatedBy: "alice" },
          { id: "sec_2", name: "NPM_TOKEN", scope: { paths: ["/acme"] } },
          // From a server older than folders.
          { id: "sec_3", name: "OLD_TOKEN", scope: { allSlices: true } },
          { id: "sec_4", name: "OLD_KEY", scope: {} }
        ]
      })
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    expect(await screen.findByText("DEPLOY_TOKEN")).toBeInTheDocument();
    expect(screen.getByText("/acme/api, /acme/web")).toBeInTheDocument();
    // Grouped by folder: NPM_TOKEN and OLD_TOKEN share /acme.
    const acme = screen.getByRole("region", { name: "/acme" });
    expect(within(acme).getByText("2 secrets")).toBeInTheDocument();
    expect(within(acme).getByText("NPM_TOKEN")).toBeInTheDocument();
    expect(within(acme).getByText("OLD_TOKEN")).toBeInTheDocument();
    expect(screen.getByText("No folders")).toBeInTheDocument();
    expect(screen.getByText(/by alice/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Actions for DEPLOY_TOKEN" })).toBeInTheDocument();
  });

  it("filters a long list by name or folder", async () => {
    apiMock.current = {
      listSecrets: vi.fn().mockResolvedValue({
        secrets: Array.from({ length: 10 }, (_, i) => ({
          id: `sec_${i}`,
          name: `TOKEN_${i}`,
          scope: { paths: [i < 5 ? "/acme/api" : "/acme/web"] }
        }))
      })
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    const filter = await screen.findByLabelText("Filter secrets");
    fireEvent.change(filter, { target: { value: "token_3" } });
    expect(screen.getByText("TOKEN_3")).toBeInTheDocument();
    expect(screen.queryByText("TOKEN_4")).not.toBeInTheDocument();
    fireEvent.change(filter, { target: { value: "acme/web" } });
    expect(screen.getAllByRole("listitem")).toHaveLength(5);
    fireEvent.change(filter, { target: { value: "nothing" } });
    expect(screen.getByText("No secret matches.")).toBeInTheDocument();
  });

  it("creates a secret on typed folders and a slice's folders", async () => {
    const createSecret = vi.fn().mockResolvedValue({ id: "sec_9" });
    apiMock.current = {
      createSecret,
      listSecrets: vi.fn().mockResolvedValue({ secrets: [] }),
      listSlices: vi.fn().mockResolvedValue(acmeSlices)
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    fireEvent.click(await screen.findByRole("button", { name: "New secret" }));

    fireEvent.change(screen.getByPlaceholderText("DEPLOY_TOKEN"), { target: { value: "deploy_token" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "s3cret" } });
    fireEvent.click(lastButton("Add secret"));
    expect(await screen.findByText("Add at least one folder.")).toBeInTheDocument();
    expect(createSecret).not.toHaveBeenCalled();

    // A folder outside the account is refused before it is sent.
    const folder = screen.getByLabelText("Add a folder");
    fireEvent.change(folder, { target: { value: "/other/x" } });
    fireEvent.click(screen.getByRole("button", { name: "Add folder" }));
    expect(await screen.findByText("A folder starts with /acme, such as /acme/services.")).toBeInTheDocument();
    fireEvent.change(folder, { target: { value: "/acme//services/billing/" } });
    fireEvent.keyDown(folder, { key: "Enter" });
    fireEvent.click(await screen.findByRole("button", { name: "web" }));
    expect(screen.getByRole("button", { name: "Remove /acme/shared/ui" })).toBeInTheDocument();

    fireEvent.click(lastButton("Add secret"));
    await waitFor(() => expect(createSecret).toHaveBeenCalled());
    expect(createSecret.mock.calls[0][0]).toEqual({
      account: "acme",
      name: "DEPLOY_TOKEN",
      scope: { paths: ["/acme/services/billing", "/acme/shared/ui", "/acme/web"] },
      value: "s3cret"
    });
  });

  it("keeps the value when editing only the folders, and shows the server's refusal", async () => {
    const updateSecret = vi.fn().mockRejectedValue(new Error("/acme/web already has secret sec_4 named DEPLOY_TOKEN"));
    apiMock.current = {
      listSecrets: vi.fn().mockResolvedValue({
        secrets: [{ id: "sec_1", name: "DEPLOY_TOKEN", scope: { paths: ["/acme/api"] } }]
      }),
      listSlices: vi.fn().mockResolvedValue(acmeSlices),
      updateSecret
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByPlaceholderText("DEPLOY_TOKEN")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Remove /acme/api" }));
    fireEvent.click(screen.getByRole("button", { name: "All of acme" }));
    fireEvent.click(screen.getByRole("button", { name: "Save secret" }));
    await waitFor(() => expect(updateSecret).toHaveBeenCalled());
    expect(updateSecret.mock.calls[0][0]).toEqual({
      id: "sec_1",
      scope: { paths: ["/acme"] },
      value: undefined
    });
    expect(await screen.findByRole("alert")).toHaveTextContent("already has secret sec_4");
  });

  it("deletes after confirming", async () => {
    const deleteSecret = vi.fn().mockResolvedValue({});
    apiMock.current = {
      deleteSecret,
      listSecrets: vi.fn().mockResolvedValue({ secrets: [{ id: "sec_2", name: "NPM_TOKEN", scope: { paths: ["/acme"] } }] })
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Checks in /acme will no longer get");
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete secret" }));
    await waitFor(() => expect(deleteSecret).toHaveBeenCalledWith({ id: "sec_2" }));
  });
});
