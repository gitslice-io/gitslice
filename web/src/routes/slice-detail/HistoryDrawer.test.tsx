import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    params?: { id?: string; account?: string };
    search?: { file?: string };
    to: string;
  }) => (
    <a
      {...(rest as Record<string, unknown>)}
      href={`${to.replace("$id", params?.id ?? "").replace("$account", params?.account ?? "")}${search?.file ? `?file=${search.file}` : ""}`}
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
    // The changeset link, and the author's link to their profile.
    expect(screen.getAllByRole("link")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "gitslice-operator" })).toHaveAttribute("href", "/accounts/gitslice-operator");
    // A commit no changeset landed is not a link.
    expect(screen.getByText("imported").closest("a")).toBeNull();
    // No list of the slice's changesets is needed any more.
    expect(api.listChangesets).not.toHaveBeenCalled();
    // Times are shown, not raw ISO strings.
    expect(screen.queryByText("2026-10-10T17:13:45.077318Z")).not.toBeInTheDocument();
    expect(document.querySelector("time")).toHaveAttribute("dateTime", "2026-10-10T17:13:45.077Z");
  });

  function commitsPage(from: number, count: number, nextPageToken = "") {
    return {
      commits: Array.from({ length: count }, (_, index) => ({
        id: `sha256:${String(from + index).padStart(4, "0")}`,
        message: `commit ${from + index}`,
        createdAt: "2026-10-01T00:00:00Z"
      })),
      nextPageToken
    };
  }

  function renderDrawer(listCommits: ReturnType<typeof vi.fn>) {
    const api = { listCommits } as unknown as ApiClient;
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <HistoryDrawer
          api={api}
          commitId="sha256:head"
          onClose={() => undefined}
          open
          selectedPath=""
          sliceId="slice_acme_app"
          sliceLabel="acme:app"
          sliceRef={{ account: "acme", slice: "app" }}
        />
      </QueryClientProvider>
    );
  }

  it("loads older commits a page at a time with the server's page token", async () => {
    const listCommits = vi
      .fn()
      .mockResolvedValueOnce(commitsPage(0, 50, "token_1"))
      .mockResolvedValueOnce(commitsPage(50, 20));
    renderDrawer(listCommits);

    expect(await screen.findByText("commit 49")).toBeInTheDocument();
    expect(screen.queryByText("commit 50")).not.toBeInTheDocument();
    expect(listCommits.mock.calls[0][0]).not.toHaveProperty("pageToken");

    fireEvent.click(screen.getByRole("button", { name: "Load older commits" }));
    expect(await screen.findByText("commit 69")).toBeInTheDocument();
    expect(screen.getByText("commit 0")).toBeInTheDocument();
    expect(listCommits).toHaveBeenLastCalledWith(
      expect.objectContaining({ limit: 50, pageToken: "token_1", slice: { account: "acme", slice: "app" } })
    );
    expect(screen.queryByRole("button", { name: "Load older commits" })).not.toBeInTheDocument();
    expect(screen.getByText("That's the whole history.")).toBeInTheDocument();
  });

  it("loads the next page when the end of the list scrolls into view", async () => {
    const observers: Array<(entries: Array<{ isIntersecting: boolean }>) => void> = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(callback: (entries: Array<{ isIntersecting: boolean }>) => void) {
          observers.push(callback);
        }
        observe() {}
        disconnect() {}
      }
    );
    try {
      const listCommits = vi
        .fn()
        .mockResolvedValueOnce(commitsPage(0, 50, "token_1"))
        .mockResolvedValueOnce(commitsPage(50, 5));
      renderDrawer(listCommits);
      await screen.findByText("commit 49");
      await waitFor(() => expect(observers.length).toBeGreaterThan(0));

      act(() => observers[observers.length - 1]([{ isIntersecting: true }]));
      expect(await screen.findByText("commit 54")).toBeInTheDocument();
      expect(listCommits).toHaveBeenCalledTimes(2);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("keeps the loaded commits when an older page fails", async () => {
    const listCommits = vi
      .fn()
      .mockResolvedValueOnce(commitsPage(0, 50, "token_1"))
      .mockRejectedValueOnce(new Error("upstream timeout"));
    renderDrawer(listCommits);
    await screen.findByText("commit 49");

    fireEvent.click(screen.getByRole("button", { name: "Load older commits" }));
    expect(await screen.findByText("Could not load older commits")).toBeInTheDocument();
    expect(screen.getByText("commit 0")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Load older commits" })).toBeEnabled();
  });
});
