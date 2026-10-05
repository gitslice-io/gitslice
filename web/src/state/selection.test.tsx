import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({ isLoaded: true, isSignedIn: true })
}));

vi.mock("../api/useApi", () => ({
  useApi: () => ({
    getAuthStatus: vi.fn().mockResolvedValue({
      accounts: ["nic", "gitslice"],
      memberships: [
        { account: "nic", kind: "personal", role: "owner" },
        { account: "gitslice", kind: "organization", role: "owner" }
      ],
      subjectId: "user_1"
    })
  })
}));

import { SelectionProvider, useSelection } from "./selection";

function Show() {
  const { activeAccount, activeMembership, setActiveAccount } = useSelection();
  return (
    <button onClick={() => setActiveAccount("gitslice")} type="button">
      {activeAccount}:{activeMembership?.kind ?? "none"}
    </button>
  );
}

function renderProvider() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <SelectionProvider>
        <Show />
      </SelectionProvider>
    </QueryClientProvider>
  );
}

beforeEach(() => window.localStorage.clear());
afterEach(cleanup);

describe("the active account", () => {
  it("starts as the personal account and remembers a switch", async () => {
    renderProvider();
    const button = await screen.findByRole("button", { name: "nic:personal" });

    button.click();
    expect(await screen.findByRole("button", { name: "gitslice:organization" })).toBeInTheDocument();
    expect(window.localStorage.getItem("gitslice.activeAccount")).toBe("gitslice");
  });

  it("restores the remembered account", async () => {
    window.localStorage.setItem("gitslice.activeAccount", "gitslice");
    renderProvider();

    expect(await screen.findByRole("button", { name: "gitslice:organization" })).toBeInTheDocument();
  });

  it("falls back to the personal account when the remembered one is not yours", async () => {
    window.localStorage.setItem("gitslice.activeAccount", "someone-else");
    renderProvider();

    await waitFor(() => expect(screen.getByRole("button", { name: "nic:personal" })).toBeInTheDocument());
  });
});
