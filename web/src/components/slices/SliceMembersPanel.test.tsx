import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  listSliceMembers: vi.fn(),
  setSliceMember: vi.fn(),
  removeSliceMember: vi.fn()
}));
vi.mock("../../api/useApi", () => ({ useApi: () => api }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, params, to, ...rest }: { children: ReactNode; params?: { account?: string }; to: string }) => (
    <a {...(rest as Record<string, unknown>)} href={to.replace("$account", params?.account ?? "")}>
      {children}
    </a>
  )
}));

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
  it("lists direct and group members and shares, changes (including contributor) and removes", async () => {
    api.listSliceMembers.mockResolvedValue({
      members: [{ kind: "agent", role: "writer", subjectId: "agent_bob", username: "bob-agent" }],
      groupMembers: [{ groupId: "grp_1", account: "acme", name: "qa", handle: "@acme/qa", role: "contributor" }]
    });
    api.setSliceMember.mockResolvedValue({});
    api.removeSliceMember.mockResolvedValue({});
    renderPanel();

    expect(await screen.findByRole("link", { name: "bob-agent" })).toHaveAttribute("href", "/accounts/bob-agent");
    expect(screen.getByText("agent")).toBeInTheDocument();
    expect(screen.getByText("@acme/qa")).toBeInTheDocument();
    expect(screen.getByText("group")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "carol" } });
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "contributor" } });
    fireEvent.click(screen.getByRole("button", { name: "Share" }));
    await waitFor(() => expect(api.setSliceMember).toHaveBeenCalledWith({ role: "contributor", slice, username: "carol" }));

    fireEvent.change(screen.getByLabelText("Role of bob-agent"), { target: { value: "admin" } });
    await waitFor(() => expect(api.setSliceMember).toHaveBeenLastCalledWith({ role: "admin", slice, username: "bob-agent" }));

    fireEvent.change(screen.getByLabelText("Role of @acme/qa"), { target: { value: "writer" } });
    await waitFor(() => expect(api.setSliceMember).toHaveBeenLastCalledWith({ role: "writer", slice, username: "@acme/qa" }));

    fireEvent.click(screen.getAllByRole("button", { name: "Remove" })[0]);
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
