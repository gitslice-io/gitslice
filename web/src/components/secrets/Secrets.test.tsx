import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SliceSecretsPanel } from "../slices/SliceSecretsPanel";
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
    expect(screen.getAllByText("/acme")).toHaveLength(2);
    expect(screen.getByText("No folders")).toBeInTheDocument();
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

describe("SliceSecretsPanel", () => {
  afterEach(cleanup);

  it("shows each secret's folders and edits only those on the slice's own", async () => {
    apiMock.current = {
      listSliceSecrets: vi.fn().mockResolvedValue({
        names: ["CI_TOKEN", "DEPLOY_TOKEN", "NPM_TOKEN"],
        secrets: [
          { name: "CI_TOKEN", paths: ["/acme/api"], secretId: "sec_1" },
          { name: "DEPLOY_TOKEN", paths: ["/acme/api", "/acme/web"], secretId: "sec_2" },
          { name: "NPM_TOKEN", paths: ["/acme"], secretId: "sec_3" }
        ]
      })
    };
    renderWithQuery(<SliceSecretsPanel hasCIDaemon includedPaths={["/acme/api"]} slice={{ account: "acme", slice: "api" }} />);
    expect(await screen.findByText("/acme/api")).toBeInTheDocument();
    expect(screen.getByText("/acme/api, /acme/web")).toBeInTheDocument();
    expect(screen.getByText("/acme")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Edit" })).toHaveLength(1);
    expect(screen.getAllByText("Edit on acme's secrets")).toHaveLength(2);
    expect(screen.queryByText("No CI daemon")).not.toBeInTheDocument();
  });

  it("adds a secret on this slice's folders and says when no daemon will receive it", async () => {
    const createSecret = vi.fn().mockResolvedValue({ id: "sec_9" });
    apiMock.current = {
      createSecret,
      listSliceSecrets: vi.fn().mockResolvedValue({ names: [] }),
      listSlices: vi.fn().mockResolvedValue(acmeSlices)
    };
    renderWithQuery(
      <SliceSecretsPanel hasCIDaemon={false} includedPaths={["/acme/web", "/acme/shared/ui"]} slice={{ account: "acme", slice: "web" }} />
    );
    expect(await screen.findByText("No CI daemon")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    expect(await screen.findByRole("button", { name: "Remove /acme/web" })).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("DEPLOY_TOKEN"), { target: { value: "CI_TOKEN" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "v" } });
    fireEvent.click(lastButton("Add secret"));
    await waitFor(() => expect(createSecret).toHaveBeenCalled());
    expect(createSecret.mock.calls[0][0]).toEqual({
      account: "acme",
      name: "CI_TOKEN",
      scope: { paths: ["/acme/shared/ui", "/acme/web"] },
      value: "v"
    });
  });
});
