import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../../api/useApi";
import { TagsDrawer, releaseHrefForTag } from "./TagsDrawer";

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
  )
}));

function renderDrawer(listTags: ReturnType<typeof vi.fn>, sliceRef = { account: "gitslice", slice: "gitslice" }) {
  const api = { listTags } as unknown as ApiClient;
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <TagsDrawer
        api={api}
        onClose={() => undefined}
        open
        sliceId="slice_1"
        sliceLabel={`${sliceRef.account}:${sliceRef.slice}`}
        sliceRef={sliceRef}
      />
    </QueryClientProvider>
  );
  return api;
}

describe("TagsDrawer", () => {
  afterEach(() => cleanup());

  it("lists the slice's tags newest first, with their release files", async () => {
    const listTags = vi.fn().mockResolvedValue({
      tags: [
        {
          name: "v0.4.33",
          commitId: "sha256:0981ca7be4206ee191ef6d1aa91bec60687821f765af7814d5e401dc32b13d97",
          message: "CLI release",
          createdBy: "gitslice-operator",
          createdAt: "2026-11-20T17:13:45Z"
        },
        { name: "nightly-1", commitId: "sha256:1111", createdAt: "2026-11-01T00:00:00Z" }
      ]
    });
    renderDrawer(listTags);

    const first = await screen.findByText("v0.4.33");
    expect(listTags).toHaveBeenCalledWith({ slice: { account: "gitslice", slice: "gitslice" } });
    expect(screen.getByText("CLI release")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Release files" })).toHaveAttribute("href", "/releases/tag/v0.4.33");
    expect(screen.getAllByRole("link", { name: "Release files" })).toHaveLength(1);
    expect(screen.getByRole("link", { name: "gitslice-operator" })).toBeInTheDocument();
    // The commit opens the slice as it was at the tag.
    expect(screen.getByTitle(/^Browse v0\.4\.33/)).toHaveAttribute(
      "href",
      "/slices/gitslice/gitslice?at=sha256:0981ca7be4206ee191ef6d1aa91bec60687821f765af7814d5e401dc32b13d97"
    );
    // Newest first, as the server listed them.
    expect(first.compareDocumentPosition(screen.getByText("nightly-1")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("links release files for aggregated gitslice/gitslice tags viewed from gitslice/home", async () => {
    const listTags = vi.fn().mockResolvedValue({
      tags: [
        {
          slice: { account: "gitslice", slice: "gitslice" },
          name: "v0.4.33",
          commitId: "sha256:0981ca7be4206ee191ef6d1aa91bec60687821f765af7814d5e401dc32b13d97",
          createdAt: "2026-11-20T17:13:45Z"
        }
      ]
    });
    renderDrawer(listTags, { account: "gitslice", slice: "home" });

    expect(await screen.findByText("v0.4.33")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Release files" })).toHaveAttribute("href", "/releases/tag/v0.4.33");
  });

  it("says how to create a tag when there are none", async () => {
    renderDrawer(vi.fn().mockResolvedValue({ tags: [] }), { account: "acme", slice: "app" });
    expect(await screen.findByText("No tags yet.")).toBeInTheDocument();
    expect(screen.getByText("gs tag create <name>")).toBeInTheDocument();
  });
});

describe("releaseHrefForTag", () => {
  it("links only Gitslice's own version tags", () => {
    const gitslice = { account: "gitslice", slice: "gitslice" };
    expect(releaseHrefForTag(gitslice, "v0.4.33")).toBe("/releases/tag/v0.4.33");
    expect(releaseHrefForTag(gitslice, "v1.0.0-rc.1")).toBe("/releases/tag/v1.0.0-rc.1");
    expect(releaseHrefForTag({ account: "acme", slice: "app" }, "v0.4.33")).toBeUndefined();
    expect(releaseHrefForTag(gitslice, "release/2026-10")).toBeUndefined();
    expect(releaseHrefForTag(undefined, "v0.4.33")).toBeUndefined();
  });
});
