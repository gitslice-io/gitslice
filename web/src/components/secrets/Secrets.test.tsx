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
  slices: [{ ref: { account: "acme", slice: "api" } }, { ref: { account: "acme", slice: "web" } }]
};

describe("AccountSecretsPanel", () => {
  afterEach(cleanup);

  it("lists secrets with the slices they reach and never a value", async () => {
    apiMock.current = {
      listSecrets: vi.fn().mockResolvedValue({
        secrets: [
          { id: "sec_1", name: "DEPLOY_TOKEN", scope: { slices: [{ account: "acme", slice: "api" }, { account: "acme", slice: "web" }] }, updatedBy: "alice" },
          { id: "sec_2", name: "NPM_TOKEN", scope: { allSlices: true } },
          { id: "sec_3", name: "OLD_KEY", scope: {} }
        ]
      })
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    expect(await screen.findByText("DEPLOY_TOKEN")).toBeInTheDocument();
    expect(screen.getByText("api, web")).toBeInTheDocument();
    expect(screen.getByText("All slices")).toBeInTheDocument();
    expect(screen.getByText("Not used by any slice")).toBeInTheDocument();
  });

  it("creates a secret scoped to chosen slices", async () => {
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
    expect(await screen.findByText("Choose at least one slice, or all slices.")).toBeInTheDocument();
    expect(createSecret).not.toHaveBeenCalled();

    fireEvent.click(await screen.findByRole("checkbox", { name: "web" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "api" }));
    fireEvent.click(lastButton("Add secret"));
    await waitFor(() => expect(createSecret).toHaveBeenCalled());
    expect(createSecret.mock.calls[0][0]).toEqual({
      account: "acme",
      name: "DEPLOY_TOKEN",
      scope: { slices: [{ account: "acme", slice: "api" }, { account: "acme", slice: "web" }] },
      value: "s3cret"
    });
  });

  it("keeps the value when editing only the scope, and shows the server's refusal", async () => {
    const updateSecret = vi.fn().mockRejectedValue(new Error("slice web already has secret sec_4 named DEPLOY_TOKEN"));
    apiMock.current = {
      listSecrets: vi.fn().mockResolvedValue({
        secrets: [{ id: "sec_1", name: "DEPLOY_TOKEN", scope: { slices: [{ account: "acme", slice: "api" }] } }]
      }),
      listSlices: vi.fn().mockResolvedValue(acmeSlices),
      updateSecret
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByPlaceholderText("DEPLOY_TOKEN")).toBeDisabled();
    fireEvent.click(await screen.findByRole("checkbox", { name: "web" }));
    fireEvent.click(screen.getByRole("button", { name: "Save secret" }));
    await waitFor(() => expect(updateSecret).toHaveBeenCalled());
    expect(updateSecret.mock.calls[0][0]).toEqual({
      id: "sec_1",
      scope: { slices: [{ account: "acme", slice: "api" }, { account: "acme", slice: "web" }] },
      value: undefined
    });
    expect(await screen.findByRole("alert")).toHaveTextContent("already has secret sec_4");
  });

  it("deletes after confirming", async () => {
    const deleteSecret = vi.fn().mockResolvedValue({});
    apiMock.current = {
      deleteSecret,
      listSecrets: vi.fn().mockResolvedValue({ secrets: [{ id: "sec_2", name: "NPM_TOKEN", scope: { allSlices: true } }] })
    };
    renderWithQuery(<AccountSecretsPanel account="acme" />);
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("every slice of acme");
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete secret" }));
    await waitFor(() => expect(deleteSecret).toHaveBeenCalledWith({ id: "sec_2" }));
  });
});

describe("SliceSecretsPanel", () => {
  afterEach(cleanup);

  it("shows where each secret comes from and edits only this slice's own", async () => {
    apiMock.current = {
      listSliceSecrets: vi.fn().mockResolvedValue({
        names: ["CI_TOKEN", "DEPLOY_TOKEN", "NPM_TOKEN"],
        secrets: [
          { name: "CI_TOKEN", secretId: "sec_1" },
          { name: "DEPLOY_TOKEN", secretId: "sec_2", sharedWith: [{ account: "acme", slice: "web" }] },
          { allSlices: true, name: "NPM_TOKEN", secretId: "sec_3" }
        ]
      })
    };
    renderWithQuery(<SliceSecretsPanel hasCIDaemon slice={{ account: "acme", slice: "api" }} />);
    expect(await screen.findByText("This slice only")).toBeInTheDocument();
    expect(screen.getByText("Shared with web")).toBeInTheDocument();
    expect(screen.getByText("All slices of acme")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Edit" })).toHaveLength(1);
    expect(screen.getAllByText("Edit on acme's secrets")).toHaveLength(2);
    expect(screen.queryByText("No CI daemon")).not.toBeInTheDocument();
  });

  it("adds a secret for this slice and says when no daemon will receive it", async () => {
    const createSecret = vi.fn().mockResolvedValue({ id: "sec_9" });
    apiMock.current = {
      createSecret,
      listSliceSecrets: vi.fn().mockResolvedValue({ names: [] }),
      listSlices: vi.fn().mockResolvedValue(acmeSlices)
    };
    renderWithQuery(<SliceSecretsPanel hasCIDaemon={false} slice={{ account: "acme", slice: "api" }} />);
    expect(await screen.findByText("No CI daemon")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    expect(await screen.findByRole("checkbox", { name: "api" })).toBeChecked();
    fireEvent.change(screen.getByPlaceholderText("DEPLOY_TOKEN"), { target: { value: "CI_TOKEN" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "v" } });
    fireEvent.click(lastButton("Add secret"));
    await waitFor(() => expect(createSecret).toHaveBeenCalled());
    expect(createSecret.mock.calls[0][0]).toEqual({
      account: "acme",
      name: "CI_TOKEN",
      scope: { slices: [{ account: "acme", slice: "api" }] },
      value: "v"
    });
  });
});
