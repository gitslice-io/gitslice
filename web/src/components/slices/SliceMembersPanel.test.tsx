import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  listSliceMembers: vi.fn(),
  setSliceMember: vi.fn(),
  removeSliceMember: vi.fn()
}));
vi.mock("../../api/useApi", () => ({ useApi: () => api }));

import { SliceMembersPanel } from "./SliceMembersPanel";

const slice = { account: "alice-agent", slice: "project" };

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SliceMembersPanel slice={slice} />
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SliceMembersPanel", () => {
  it("lists members and shares, changes and removes", async () => {
    api.listSliceMembers.mockResolvedValue({
      members: [{ kind: "agent", role: "writer", subjectId: "agent_bob", username: "bob-agent" }]
    });
    api.setSliceMember.mockResolvedValue({});
    api.removeSliceMember.mockResolvedValue({});
    renderPanel();

    expect(await screen.findByText("bob-agent")).toBeInTheDocument();
    expect(screen.getByText("agent")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "carol" } });
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "reader" } });
    fireEvent.click(screen.getByRole("button", { name: "Share" }));
    await waitFor(() => expect(api.setSliceMember).toHaveBeenCalledWith({ role: "reader", slice, username: "carol" }));

    fireEvent.change(screen.getByLabelText("Role of bob-agent"), { target: { value: "admin" } });
    await waitFor(() => expect(api.setSliceMember).toHaveBeenLastCalledWith({ role: "admin", slice, username: "bob-agent" }));

    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.removeSliceMember).toHaveBeenCalledWith({ slice, username: "bob-agent" }));
  });

  it("says when the slice is not shared, and shows errors", async () => {
    api.listSliceMembers.mockResolvedValue({ members: [] });
    api.setSliceMember.mockRejectedValue(new Error("no user or agent named nobody"));
    renderPanel();
    expect(await screen.findByText("Not shared with anyone outside alice-agent.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "nobody" } });
    fireEvent.click(screen.getByRole("button", { name: "Share" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("no user or agent named nobody");
  });
});
