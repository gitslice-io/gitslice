import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ listChangesets: vi.fn() }));
const router = vi.hoisted(() => ({
  navigate: vi.fn(),
  search: { slice: "gitslice/gitslice" } as Record<string, unknown>
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({ isLoaded: true, isSignedIn: false })
}));
vi.mock("../api/useApi", () => ({ useApi: () => api }));
vi.mock("../state/selection", () => ({ useSelection: () => ({ accounts: [] }) }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    params,
    to,
    ...rest
  }: {
    children: ReactNode;
    params?: { id?: string; account?: string };
    to: string;
  }) => (
    <a {...(rest as Record<string, unknown>)} href={to.replace("$id", params?.id ?? "").replace("$account", params?.account ?? "")}>
      {children}
    </a>
  ),
  useNavigate: () => router.navigate,
  useSearch: () => router.search
}));

import { ChangesetsPage } from "./ChangesetsPage";

const openStatuses = ["draft", "open", "published", "pending_publish"];

function renderPage() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ChangesetsPage />
    </QueryClientProvider>
  );
}

// The search a navigate call asked for, given the address it is applied to.
function navigatedSearch(call: number, replace: boolean, from: Record<string, unknown> = router.search) {
  const { search, replace: replaced, to } = router.navigate.mock.calls[call][0];
  expect(replaced).toBe(replace);
  expect(to).toBe("/changesets");
  return search(from) as Record<string, unknown>;
}

beforeEach(() => {
  router.search = { slice: "gitslice/gitslice" };
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("changesets list", () => {
  it("names each changeset's author, linked to their profile, in the Author column and under the title on narrow screens", async () => {
    api.listChangesets.mockResolvedValue({
      changesets: [
        { id: "cs_0123456789abcdef", number: 1, title: "web: first", author: "sunny", status: "submitted" },
        { id: "cs_fedcba9876543210", number: 2, title: "web: second", author: "user_ext_7cac3e86bbbe5541", status: "draft" }
      ]
    });
    renderPage();

    const first = (await screen.findByText("web: first")).closest("tr") as HTMLElement;
    // Both places link to the profile: the md-and-up column and the
    // below-md line under the title (CSS shows one of them).
    const links = within(first).getAllByRole("link", { name: "sunny" });
    expect(links).toHaveLength(2);
    links.forEach((link) => expect(link).toHaveAttribute("href", "/accounts/sunny"));
    expect(first.querySelector("td:first-child .md\\:hidden")).toHaveTextContent(/^by\s*sunny$/);

    // An author the server could not name is shown as it came, without a link.
    const second = screen.getByText("web: second").closest("tr") as HTMLElement;
    expect(within(second).getAllByText("user_ext_7cac3e86bbbe5541")).toHaveLength(2);
    expect(within(second).queryByRole("link", { name: "user_ext_7cac3e86bbbe5541" })).toBeNull();
  });

  it("starts with the open changesets and pages through the rest with Show more", async () => {
    api.listChangesets
      .mockResolvedValueOnce({ changesets: [{ id: "cs_aaaaaaaaaaaaaaaa", title: "newer", status: "draft" }], nextPageToken: "t1" })
      .mockResolvedValueOnce({ changesets: [{ id: "cs_bbbbbbbbbbbbbbbb", title: "older", status: "draft" }] });
    renderPage();

    expect(await screen.findByText("newer")).toBeInTheDocument();
    expect(api.listChangesets).toHaveBeenCalledTimes(1);
    expect(api.listChangesets.mock.calls[0][0]).toEqual({
      authoringSlice: { account: "gitslice", slice: "gitslice" },
      summary: true,
      limit: 50,
      statuses: openStatuses
    });
    expect(screen.getByRole("button", { name: "Open" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("1 changeset so far")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("older")).toBeInTheDocument();
    expect(api.listChangesets.mock.calls[1][0]).toEqual(expect.objectContaining({ pageToken: "t1", statuses: openStatuses }));
    // Both pages, in the server's order, and nothing further to ask for.
    expect(screen.getAllByRole("row").slice(1).map((row) => row.textContent)).toEqual([
      expect.stringContaining("newer"),
      expect.stringContaining("older")
    ]);
    expect(screen.queryByRole("button", { name: "Show more" })).toBeNull();
    expect(screen.getByText("2 changesets")).toBeInTheDocument();
  });

  it("writes the status tab and the typed search to the address, leaving the defaults out of it", async () => {
    api.listChangesets.mockResolvedValue({ changesets: [{ id: "cs_aaaaaaaaaaaaaaaa", title: "one", status: "draft" }] });
    renderPage();
    await screen.findByText("one");

    fireEvent.click(screen.getByRole("button", { name: "Landed" }));
    // A tab adds a history entry, so Back returns to the one before.
    expect(navigatedSearch(0, false)).toEqual({ slice: "gitslice/gitslice", status: "landed", q: undefined });
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(navigatedSearch(1, false)).toEqual({ slice: "gitslice/gitslice", status: "all", q: undefined });
    // Open is the default, so it is not in the address.
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(navigatedSearch(2, false, { slice: "gitslice/gitslice", status: "landed" })).toEqual({
      slice: "gitslice/gitslice",
      status: undefined,
      q: undefined
    });

    // Typing waits a moment, and then asks once.
    const box = screen.getByRole("searchbox", { name: "Search changesets" });
    router.navigate.mockClear();
    fireEvent.change(box, { target: { value: "b" } });
    fireEvent.change(box, { target: { value: "blame author:sunny" } });
    expect(router.navigate).not.toHaveBeenCalled();
    await waitFor(() => expect(router.navigate).toHaveBeenCalledTimes(1));
    // Typing refines the entry you are on.
    expect(navigatedSearch(0, true)).toEqual({ slice: "gitslice/gitslice", status: undefined, q: "blame author:sunny" });
  });

  it("lists what the address asks for, and offers every changeset when nothing matches", async () => {
    router.search = { slice: "gitslice/gitslice", status: "landed", q: "fix author:Sunny" };
    api.listChangesets.mockResolvedValue({ changesets: [] });
    renderPage();

    expect(await screen.findByText("No changesets match.")).toBeInTheDocument();
    expect(api.listChangesets.mock.calls[0][0]).toEqual({
      authoringSlice: { account: "gitslice", slice: "gitslice" },
      summary: true,
      limit: 50,
      statuses: ["submitted", "merged"],
      query: "fix",
      author: "sunny"
    });
    expect(screen.getByRole("button", { name: "Landed" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("searchbox", { name: "Search changesets" })).toHaveValue("fix author:Sunny");

    fireEvent.click(screen.getByRole("button", { name: "Show all changesets" }));
    expect(navigatedSearch(0, false)).toEqual({ slice: "gitslice/gitslice", status: "all", q: undefined });
  });

  it("says a slice with no changesets has none yet, not that nothing matches", async () => {
    api.listChangesets.mockResolvedValue({ changesets: [] });
    renderPage();
    expect(await screen.findByText("No changesets for this slice yet.")).toBeInTheDocument();
  });

  it("keeps the list when loading more fails", async () => {
    api.listChangesets
      .mockResolvedValueOnce({ changesets: [{ id: "cs_aaaaaaaaaaaaaaaa", title: "kept", status: "draft" }], nextPageToken: "t1" })
      .mockRejectedValueOnce(new Error("network down"));
    renderPage();
    await screen.findByText("kept");

    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("Could not load more changesets")).toBeInTheDocument();
    expect(screen.getByText("kept")).toBeInTheDocument();
  });
});
