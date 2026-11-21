import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../api/useApi";
import { TagsDrawer, releaseHrefForTag, tagSummary } from "./TagsDrawer";

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    params,
    search,
    to,
    ...rest
  }: {
    children: ReactNode;
    params?: { account?: string; slice?: string };
    search?: { at?: string };
    to: string;
  }) => (
    <a
      {...(rest as Record<string, unknown>)}
      href={`${to.replace("$account", params?.account ?? "").replace("$slice", params?.slice ?? "")}${search?.at ? `?at=${search.at}` : ""}`}
    >
      {children}
    </a>
  ),
}));

function renderDrawer(
  listTags: ReturnType<typeof vi.fn>,
  sliceRef = { account: "gitslice", slice: "gitslice" },
  { open = true, onClose = () => undefined }: { open?: boolean; onClose?: () => void } = {},
) {
  const api = { listTags } as unknown as ApiClient;
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <TagsDrawer
        api={api}
        onClose={onClose}
        open={open}
        sliceId="slice_1"
        sliceLabel={`${sliceRef.account}:${sliceRef.slice}`}
        sliceRef={sliceRef}
      />
    </QueryClientProvider>,
  );
  return api;
}

describe("TagsDrawer", () => {
  afterEach(() => cleanup());

  it("links the tag name to browse at the tag commit and keeps commit/author inside expandable details", async () => {
    const commitId =
      "sha256:0981ca7be4206ee191ef6d1aa91bec60687821f765af7814d5e401dc32b13d97";
    const listTags = vi.fn().mockResolvedValue({
      tags: [
        {
          slice: { account: "gitslice", slice: "gitslice" },
          name: "v0.4.33",
          commitId,
          message: "CLI release",
          createdBy: "gitslice-operator",
          createdAt: "2026-11-20T17:13:45Z",
        },
        {
          name: "nightly-1",
          commitId: "sha256:1111",
          createdAt: "2026-11-01T00:00:00Z",
        },
      ],
    });
    renderDrawer(listTags);

    const first = await screen.findByRole("link", { name: "v0.4.33" });
    expect(listTags).toHaveBeenCalledWith({
      slice: { account: "gitslice", slice: "gitslice" },
    });
    expect(first).toHaveAttribute(
      "href",
      `/slices/gitslice/gitslice?at=${commitId}`,
    );
    // Compact collapsed view: summary and release download link are visible,
    // while repetitive commit hash and author stay hidden until expanded.
    expect(screen.getByText("CLI release")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Release files for v0.4.33" }),
    ).toHaveAttribute("href", "/releases/tag/v0.4.33");
    expect(
      screen.queryByRole("link", { name: "gitslice-operator" }),
    ).not.toBeInTheDocument();

    // Expanding reveals the short commit hash and author.
    const toggle = screen.getByRole("button", { name: "Details for v0.4.33" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "0981ca7" })).toHaveAttribute(
      "href",
      `/slices/gitslice/gitslice?at=${commitId}`,
    );
    expect(
      screen.getByRole("link", { name: "gitslice-operator" }),
    ).toBeInTheDocument();

    // Newest first, as the server listed them.
    expect(
      first.compareDocumentPosition(screen.getByRole("link", { name: "nightly-1" })) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("shows originating slice badges, slice filter pills, and sub-slice links on home", async () => {
    const commitId =
      "sha256:0981ca7be4206ee191ef6d1aa91bec60687821f765af7814d5e401dc32b13d97";
    const listTags = vi.fn().mockResolvedValue({
      tags: [
        {
          slice: { account: "gitslice", slice: "gitslice" },
          name: "v0.4.33",
          commitId,
          createdAt: "2026-11-20T17:13:45Z",
        },
        {
          slice: { account: "gitslice", slice: "home" },
          name: "home-snap",
          commitId: "sha256:2222",
          createdAt: "2026-11-19T00:00:00Z",
        },
      ],
    });
    renderDrawer(listTags, { account: "gitslice", slice: "home" });

    expect(
      await screen.findByRole("link", { name: "v0.4.33" }),
    ).toHaveAttribute("href", `/slices/gitslice/home?at=${commitId}`);
    expect(
      screen.getByRole("link", { name: "Release files for v0.4.33" }),
    ).toHaveAttribute("href", "/releases/tag/v0.4.33");

    // Multi-slice filter pills appear on home and narrow the list.
    const homeFilter = screen.getByRole("button", { name: /^home\s+1$/ });
    fireEvent.click(homeFilter);
    expect(homeFilter).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByRole("link", { name: "v0.4.33" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "home-snap" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(screen.getByRole("link", { name: "v0.4.33" })).toBeInTheDocument();

    // Expanding a sub-slice tag offers a direct link to open that slice at the tag.
    fireEvent.click(screen.getByRole("button", { name: "Details for v0.4.33" }));
    expect(screen.getByRole("link", { name: "Open gitslice" })).toHaveAttribute(
      "href",
      `/slices/gitslice/gitslice?at=${commitId}`,
    );
  });

  it("filters tags by name or message when there are more than 5 tags", async () => {
    const tags = Array.from({ length: 6 }, (_, i) => ({
      slice: { account: "gitslice", slice: "gitslice" },
      name: `v0.4.${30 + i}`,
      commitId: `sha256:${i}111111`,
      message: i === 2 ? "lazy mount readahead fix" : "routine release",
      createdAt: `2026-11-1${i}T00:00:00Z`,
    }));
    renderDrawer(vi.fn().mockResolvedValue({ tags }));

    const filterInput = await screen.findByLabelText("Filter tags");
    fireEvent.change(filterInput, { target: { value: "readahead" } });
    expect(screen.getByRole("link", { name: "v0.4.32" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "v0.4.30" })).not.toBeInTheDocument();

    fireEvent.change(filterInput, { target: { value: "does-not-exist" } });
    expect(screen.getByText("No matching tags.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByRole("link", { name: "v0.4.30" })).toBeInTheDocument();
  });

  it("dismisses on Escape key", async () => {
    const onClose = vi.fn();
    renderDrawer(vi.fn().mockResolvedValue({ tags: [] }), undefined, { onClose });
    await screen.findByText("No tags yet.");
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("says how to create a tag when there are none", async () => {
    renderDrawer(vi.fn().mockResolvedValue({ tags: [] }), {
      account: "acme",
      slice: "app",
    });
    expect(await screen.findByText("No tags yet.")).toBeInTheDocument();
    expect(screen.getByText("gs tag create <name>")).toBeInTheDocument();
  });

  it("shows one line of a long message until tapped, without repeating the name", async () => {
    const listTags = vi.fn().mockResolvedValue({
      tags: [
        {
          name: "v0.4.31",
          commitId: "sha256:2222",
          message: "gs v0.4.31: listing deltas\nand cache pruning",
        },
      ],
    });
    renderDrawer(listTags);

    const message = await screen.findByRole("button", {
      name: /^listing deltas/,
    });
    expect(message).toHaveAttribute("aria-expanded", "false");
    expect(message).toHaveClass("truncate");
    fireEvent.click(message);
    expect(
      screen.getByText(/listing deltas\s+and cache pruning/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Details for v0.4.31" }),
    ).toHaveAttribute("aria-expanded", "true");
  });
});

describe("releaseHrefForTag", () => {
  it("links only Gitslice's own version tags", () => {
    const gitslice = { account: "gitslice", slice: "gitslice" };
    expect(releaseHrefForTag(gitslice, "v0.4.33")).toBe(
      "/releases/tag/v0.4.33",
    );
    expect(releaseHrefForTag(gitslice, "v1.0.0-rc.1")).toBe(
      "/releases/tag/v1.0.0-rc.1",
    );
    expect(
      releaseHrefForTag({ account: "acme", slice: "app" }, "v0.4.33"),
    ).toBeUndefined();
    expect(releaseHrefForTag(gitslice, "release/2026-10")).toBeUndefined();
    expect(releaseHrefForTag(undefined, "v0.4.33")).toBeUndefined();
  });
});

describe("tagSummary", () => {
  it("drops a leading tag name", () => {
    expect(tagSummary("v1.2.3", "v1.2.3: fixes")).toBe("fixes");
    expect(tagSummary("v1.2.3", "gs v1.2.3: fixes")).toBe("fixes");
    expect(tagSummary("v1.2.3", "Release v1.2.3: fixes")).toBe(
      "Release v1.2.3: fixes",
    );
    expect(tagSummary("", ": x")).toBe(": x");
  });
});
