import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../api/useApi";
import { HistoryDrawer } from "./HistoryDrawer";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    params,
    search,
    to,
    ...rest
  }: {
    children: ReactNode;
    params?: { id?: string };
    search?: { file?: string };
    to: string;
  }) => (
    <a
      {...(rest as Record<string, unknown>)}
      href={`${to.replace("$id", params?.id ?? "")}${search?.file ? `?file=${search.file}` : ""}`}
    >
      {children}
    </a>
  )
}));

describe("HistoryDrawer", () => {
  afterEach(() => cleanup());

  it("links each commit to the diff of the changeset that landed it, whichever slice authored it", async () => {
    const listCommits = vi.fn().mockResolvedValue({
      commits: [
        {
          id: "sha256:0981ca7be4206ee191ef6d1aa91bec60687821f765af7814d5e401dc32b13d97",
          message: "web: Home lists only the active account",
          author: "gitslice-operator",
          createdAt: "2026-10-10T17:13:45.077318Z",
          // Authored in gitslice/gitslice, viewed from gitslice/home.
          changesetId: "cs_b083ab0e70aaaaaaaaaaaaaaaaaaaaaaaa"
        },
        { id: "sha256:1111", message: "imported", createdAt: "2026-10-01T00:00:00Z" }
      ]
    });
    const api = { listCommits, listChangesets: vi.fn() } as unknown as ApiClient;
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <HistoryDrawer
          api={api}
          commitId="sha256:head"
          onClose={() => undefined}
          open
          selectedPath="/gitslice/gitslice/web/src/routes/HomePage.tsx"
          sliceId="slice_gitslice_home"
          sliceLabel="gitslice:home"
          sliceRef={{ account: "gitslice", slice: "home" }}
        />
      </QueryClientProvider>
    );

    // The title is the link: no separate button.
    const link = await screen.findByRole("link", { name: "web: Home lists only the active account" });
    expect(link).toHaveAttribute("href", "/cs/b083ab0e70?file=gitslice/gitslice/web/src/routes/HomePage.tsx");
    expect(screen.getAllByRole("link")).toHaveLength(1);
    // A commit no changeset landed is not a link.
    expect(screen.getByText("imported").closest("a")).toBeNull();
    // No list of the slice's changesets is needed any more.
    expect(api.listChangesets).not.toHaveBeenCalled();
    // Times are shown, not raw ISO strings.
    expect(screen.queryByText("2026-10-10T17:13:45.077318Z")).not.toBeInTheDocument();
    expect(document.querySelector("time")).toHaveAttribute("dateTime", "2026-10-10T17:13:45.077Z");
  });
});
