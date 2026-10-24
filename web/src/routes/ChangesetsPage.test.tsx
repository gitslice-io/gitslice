import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ listChangesets: vi.fn() }));

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
  useNavigate: () => vi.fn(),
  useSearch: () => ({ slice: "gitslice/gitslice" })
}));

import { ChangesetsPage } from "./ChangesetsPage";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("changesets list", () => {
  it("names each changeset's author, linked to their profile, in the Author column and under the title on narrow screens", async () => {
    api.listChangesets.mockResolvedValue({
      changesets: [
        { id: "cs_0123456789abcdef", number: 1, title: "web: first", author: "sunny", status: "submitted" },
        { id: "cs_fedcba9876543210", number: 2, title: "web: second", author: "user_ext_7cac3e86bbbe5541", status: "open" }
      ]
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ChangesetsPage />
      </QueryClientProvider>
    );

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
});
